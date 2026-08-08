// Package run implements the persisted executor: the component that takes a created run and
// advances it — engine wake, plan, generate_sql, static_check, execute, shadow_check (FEAT-031),
// adversarial_review (FEAT-032), chart_spec, narrate, then Report materialization — writing every
// boundary to real runs/steps rows as it goes.
//
// This is the Phase 2 boundary in concrete form (IMPLEMENTATION.md "Phase 2 boundary assumptions"):
// a LINEAR executor with no leases, heartbeats, or resume (resume arrived with FEAT-022). A step is
// created the moment its work begins and completed or failed in a single follow-up write; the
// per-step idempotency key (runID:stepName) is persisted so the schema's retry-safety guarantee is
// present, and the orchestrator step machine replays completed work on resume (FEAT-022). reconcile
// is measured inside execute (its data folds into the Report) rather than as its own step row, and
// assemble is the Report materialization itself; both follow architecture.md's PipelineOrder.
//
// This package is the ONLY route to the model that exists at runtime: the executor builds every
// metadata payload through model.BuildPayload and every facts payload through model.BuildFactsPayload
// (the two shared construction functions risk_log.md R-016 requires — see model/payload.go's package
// doc for why the type system enforces them) and transmits them through model.Client, which writes
// the egress log in the same code path as the outbound call (behavior rule 7). The one verification
// layer with an extra model call is adversarial_review (FEAT-032 AC3); shadow_check (FEAT-031) is
// deterministic engine computation only.
package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/orchestrator"
	"github.com/ahmadkhidir/groundwork/server/internal/semantic"
	"github.com/ahmadkhidir/groundwork/server/internal/storage"
)

// Narrow seams, defined consumer-side so this package is unit-testable without Postgres, DuckDB, or
// the Anthropic API. Each is satisfied by one concrete repository/client in cmd/api/main.go.

// RunStore persists and advances run rows. Satisfied by postgres.RunRepository.
type RunStore interface {
	GetByID(ctx context.Context, id string) (domain.Run, error)
	UpdateStatus(ctx context.Context, id string, status domain.RunStatus) error
}

// StepStore persists step rows. Satisfied by postgres.StepRepository. It is deliberately the same
// width as orchestrator.StepStore — the executor hands this seam to the orchestrator engine, which
// drives the per-step claim/persist/replay loop (FEAT-022) — plus two methods the orchestrator
// must NOT own: Halt and InvalidateFromStep are the halt/amend writes (FEAT-024), which
// deliberately exceed orchestrator.Reset's "a completed step is never reset" guarantee. Keeping
// them off orchestrator.StepStore preserves that guarantee for plain retries while letting the
// executor's own halt/amend entry points settle and re-claim rows.
type StepStore interface {
	ListByRun(ctx context.Context, runID string) ([]domain.Step, error)
	Create(ctx context.Context, workspaceID string, step domain.Step) error
	Reset(ctx context.Context, id string) error
	Complete(ctx context.Context, id, output string) error
	Fail(ctx context.Context, id, reason string) error
	// Halt settles an in-flight step to halted (FEAT-024). A completed row is left untouched.
	Halt(ctx context.Context, id string) error
	// InvalidateFromStep re-claims stepName and every step after it in pipeline order into a fresh
	// running attempt, so an amendment re-runs its downstream (FEAT-024). Existing rows only — the
	// idempotency_key UNIQUE constraint is never violated.
	InvalidateFromStep(ctx context.Context, runID string, stepName domain.StepName) error
}

// DatasetStore lists a workspace's datasets. Satisfied by postgres.DatasetRepository.
type DatasetStore interface {
	ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error)
}

// ColumnStore lists a dataset's profiled columns. Satisfied by postgres.ColumnRepository.
type ColumnStore interface {
	ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error)
}

// ReportStore persists a completed run's Report object. Satisfied by postgres.ReportRepository.
//
// This is the materialization seam FEAT-014's report boundary names: when every step has succeeded,
// the executor writes the Report row itself, in the same completion path — a completed run without
// its Report is a corrupt state, so a persistence failure here fails the run rather than being
// swallowed.
//
// GetByRunID backs FEAT-021/FEAT-025's parent-report linkage: a forked run's report records its
// parent report by resolving the run's parent_run_id to the parent run's materialized report (empty
// when the parent has none yet — the link is honest, never fabricated).
type ReportStore interface {
	Create(ctx context.Context, report domain.Report) error
	GetByRunID(ctx context.Context, workspaceID, runID string) (domain.Report, error)
}

// ProvenanceStore persists and reads claim-level provenance links (FEAT-030). Satisfied by
// postgres.ProvenanceRepository.
//
// The executor writes links in the same report-materialization path that writes the Report row (a
// Report whose claims could not be persisted is a corrupt state, so WriteLinks failing there fails
// the run). The API reads them back to serve the provenance-panel detail endpoint.
type ProvenanceStore interface {
	WriteLinks(ctx context.Context, workspaceID string, links []domain.ProvenanceLink) error
	ListByReport(ctx context.Context, reportID string) ([]domain.ProvenanceLink, error)
	GetByID(ctx context.Context, workspaceID, id string) (domain.ProvenanceLink, error)
}

// ObjectStore reads dataset files from object storage. Satisfied by storage.Client.
type ObjectStore interface {
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
}

// EngineProvider leases a ready engine for a workspace. Satisfied by engine.Provider.
//
// Wider than api.EngineStatusProvider on purpose: the executor genuinely acquires and releases
// (its work is the whole reason an engine gets woken), whereas the HTTP status seam must never
// trigger a wake. Status is included so a run can report "waking" only when the provider actually
// is waking — see execute's use.
type EngineProvider interface {
	Acquire(ctx context.Context, workspaceID string) (engine.Handle, error)
	Release(handle engine.Handle) error
	Status(ctx context.Context, workspaceID string) (engine.Status, error)
}

// AuditWriter records workspace-scoped actions from the run's own code path. Satisfied by
// internal/audit.Writer.
type AuditWriter interface {
	WriteModelCall(ctx context.Context, workspaceID, userID, payload string) error
	WriteQueryExecuted(ctx context.Context, workspaceID, userID string) error
}

// ModelClient is the outbound model seam. Satisfied by model.Client.
type ModelClient interface {
	Complete(ctx context.Context, workspaceID, userID, system string, payload model.Payload) (string, error)
}

// Executor advances runs through their three-step pipeline. Safe for concurrent use: each Submit
// spawns its own goroutine, and engine-provider serialization (one handle per workspace) is what
// keeps same-workspace runs from interleaving on a handle that is explicitly not concurrency-safe.
type Executor struct {
	runs     RunStore
	steps    StepStore
	datasets DatasetStore
	columns  ColumnStore
	objects  ObjectStore
	provider EngineProvider
	audit    AuditWriter
	client   ModelClient
	reports  ReportStore
	logger   *slog.Logger

	// engine is the durable step machine (internal/orchestrator) this seam drives: it owns step
	// persistence, resume-skip, and per-step live event emission. Built in NewExecutor/rebuildEngine.
	engine *orchestrator.Engine
	// hub, when wired (WithEvents), receives the run-status and cache events this seam publishes —
	// the same hub the SSE run-event stream subscribes to (FEAT-023).
	hub *events.Hub
	// cache, when wired (WithCache), enables determinism caching (FEAT-027).
	cache orchestrator.CacheStore
	// provenance, when wired (WithProvenance), persists the claim-level provenance links a run's
	// narrate step produced (FEAT-030). Left nil in tests and paths that don't need link storage; a
	// nil provenance store means claims resolve in-memory but are not persisted.
	provenance ProvenanceStore

	// Semantic-layer listers feed semantic.AssembleGroundingContext (FEAT-019's building block).
	// Typed as the semantic package's own interfaces because that is exactly the contract
	// AssembleGroundingContext consumes; the same postgres repositories satisfy them.
	relationships semantic.RelationshipLister
	metrics       semantic.MetricDefinitionLister
	corrections   semantic.CorrectionLister

	// cancels maps runID → the cancel func of its background execution context. Registering a
	// cancellable context per run is what makes Halt (FEAT-024) real: cancelling it aborts the
	// current step's model/engine work (both honor ctx cancellation) instead of letting the query
	// run to completion unobserved.
	cancelMu sync.RWMutex
	cancels  map[string]context.CancelFunc
}

// Option configures an Executor at construction. Options run before the internal step engine is
// built, so a WithEvents/WithCache option's effects are wired into it.
type Option func(*Executor)

// WithEvents wires the executor to a live-event hub (FEAT-023): run-status, step, and cache events
// are published to hub's per-run fan-out, which the SSE run-event stream endpoint subscribes to.
func WithEvents(hub *events.Hub) Option {
	return func(e *Executor) { e.hub = hub }
}

// WithCache enables determinism caching (FEAT-027) against the given store. Without it, runs always
// execute fresh (tests and keyless dev servers leave it unwired).
func WithCache(cache orchestrator.CacheStore) Option {
	return func(e *Executor) { e.cache = cache }
}

// WithProvenance wires the claim-level provenance store (FEAT-030) into report materialization. When
// it is not wired, a run's resolved claims still produce a provenance_links array on the Report but
// the backing links are not persisted (tests, dev without the store).
func WithProvenance(store ProvenanceStore) Option {
	return func(e *Executor) { e.provenance = store }
}

// NewExecutor constructs an Executor from its seams.
func NewExecutor(
	runs RunStore,
	steps StepStore,
	datasets DatasetStore,
	columns ColumnStore,
	objects ObjectStore,
	provider EngineProvider,
	audit AuditWriter,
	client ModelClient,
	reports ReportStore,
	relationships semantic.RelationshipLister,
	metrics semantic.MetricDefinitionLister,
	corrections semantic.CorrectionLister,
	logger *slog.Logger,
	opts ...Option,
) *Executor {
	e := &Executor{
		runs:          runs,
		steps:         steps,
		datasets:      datasets,
		columns:       columns,
		objects:       objects,
		provider:      provider,
		audit:         audit,
		client:        client,
		reports:       reports,
		logger:        logger,
		relationships: relationships,
		metrics:       metrics,
		corrections:   corrections,
		cancels:       make(map[string]context.CancelFunc),
	}
	for _, opt := range opts {
		opt(e)
	}
	e.rebuildEngine()
	return e
}

// rebuildEngine (re)builds the durable step machine with the executor's current seams and emitter.
func (e *Executor) rebuildEngine() {
	e.engine = orchestrator.NewEngine(e.steps, emitFunc(e.publish), e.logger)
}

// publish fans a live event out to the hub's subscribers for runID, if a hub is wired. Safe to call
// with a nil hub (tests).
func (e *Executor) publish(runID string, ev events.Event) {
	if e.hub != nil {
		e.hub.Publish(runID, ev)
	}
}

// runContext returns a per-run cancellable context for background execution and registers its cancel
// func so Halt can stop in-flight work (FEAT-024): every model call and engine query in this run's
// pipeline is issued with this context, so cancelling it aborts the current step's work instead of
// letting a query run to completion unobserved.
//
// The returned cancel func must be released with releaseRunContext when the run's goroutine ends,
// so the registry never leaks an entry for a finished run.
func (e *Executor) runContext(runID string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancelMu.Lock()
	e.cancels[runID] = cancel
	e.cancelMu.Unlock()
	return ctx, cancel
}

// releaseRunContext drops runID's registered cancel func once its execution goroutine has finished.
// A later Halt of an already-terminal run is a no-op anyway (Halt guards on run status), so the
// missing entry is never consulted.
func (e *Executor) releaseRunContext(runID string) {
	e.cancelMu.Lock()
	delete(e.cancels, runID)
	e.cancelMu.Unlock()
}

// cancelRun invokes runID's registered cancel func, if any. This is the load-bearing half of Halt
// (FEAT-024): the in-flight step's model/engine work observes the cancelled context and returns
// immediately, after which the executor settles the step to halted rather than failing it.
func (e *Executor) cancelRun(runID string) {
	e.cancelMu.RLock()
	cancel := e.cancels[runID]
	e.cancelMu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

// emitFunc adapts Executor.publish to orchestrator.Emitter.
type emitFunc func(runID string, ev events.Event)

func (f emitFunc) Publish(runID string, ev events.Event) { f(runID, ev) }

// setRunStatus persists a run-status transition and publishes it to the live stream (FEAT-023).
func (e *Executor) setRunStatus(ctx context.Context, log *slog.Logger, runID string, status domain.RunStatus) error {
	if err := e.runs.UpdateStatus(ctx, runID, status); err != nil {
		return err
	}
	e.publish(runID, events.Event{Type: events.TypeRunStatus, RunID: runID, Status: string(status)})
	return nil
}

// Submit starts run's background execution.
//
// It returns immediately: runs are server-side state (architecture.md), so execution continues even
// if the submitting user navigates away. userID is the run's initiating user, needed for the
// workspace-scoped audit entries (domain.Run carries no user field).
func (e *Executor) Submit(userID string, run domain.Run) {
	ctx, cancel := e.runContext(run.ID)
	go func() {
		defer cancel()
		defer e.releaseRunContext(run.ID)
		e.execute(ctx, userID, run)
	}()
}

// SubmitWithSQL starts run's background execution with generate_sql seeded by the user's edited SQL
// (FEAT-018's Phase 2 boundary, IMPLEMENTATION.md "Phase 2 boundary assumptions": edited SQL starts
// a NEW run carrying the user's SQL as generate_sql output).
//
// The seeded path deliberately skips plan and the model's generate_sql call entirely: the edited
// query IS the plan, so no plan step row is created and no model round-trip decides what to run. The
// run then continues through the same execute → shadow_check → adversarial_review → chart_spec →
// narrate → report materialization tail as a normal run, so the resulting Report is built from
// exactly the edited query's result set.
//
// sql is trimmed before being persisted; a whitespace-only value fails the run at the generate_sql
// step with an honest message rather than executing nothing.
func (e *Executor) SubmitWithSQL(userID string, run domain.Run, sql string) {
	ctx, cancel := e.runContext(run.ID)
	go func() {
		defer cancel()
		defer e.releaseRunContext(run.ID)
		e.executeSeeded(ctx, userID, run, sql)
	}()
}

// stepContext carries the identity fields every step write needs, plus the resume ledger of already-
// completed steps loaded at the top of a pipeline run.
type stepContext struct {
	runID       string
	workspaceID string
	userID      string
	// ledger maps stepName → the run's most recent persisted row for that name: a COMPLETED step is
	// replayed by the step machine, a failed/stale row is reclaimed — a resumed run picks up from
	// its first unfinished step instead of re-running (and double-executing) prior work (FEAT-022).
	ledger map[domain.StepName]domain.Step
}

// execute advances run through its pipeline: wake the engine, plan, generate_sql, execute, then a
// single terminal status. It is the normal (model-driven) path; see executeSeeded for FEAT-018.
func (e *Executor) execute(ctx context.Context, userID string, run domain.Run) {
	e.executePipeline(ctx, userID, run, nil, true, "")
}

// executeSeeded is the FEAT-018 path: the run's generate_sql step is seeded with the user's edited
// SQL instead of being produced by plan + the model. includePlan is false (no plan step row).
func (e *Executor) executeSeeded(ctx context.Context, userID string, run domain.Run, sql string) {
	e.executePipeline(ctx, userID, run, &sql, false, "")
}

// executePipeline advances run through its pipeline to a single terminal status. seedSQL is nil for
// a normal run (plan → model generate_sql) and non-nil for FEAT-018's edited-SQL path (generate_sql
// seeded directly, plan skipped entirely). includePlan is true for a normal run (a plan step exists)
// and false for a seeded run or for a resume whose plan step was never persisted (it has already
// completed generate_sql). clarification is the user's FEAT-033 answer when this execution resumes a
// run that paused for a clarifying question ("" otherwise); it is folded into the determinism-cache
// seed so a clarified execution never replays — or shadows — an unclarified one. Every branch that
// cannot continue marks the run failed (FEAT-022: the pipeline is idempotent+resumable — every step
// is skipped when a completed row already exists, so re-entering it (Resume) replays done work and
// only executes from the first unfinished step).
func (e *Executor) executePipeline(ctx context.Context, userID string, run domain.Run, seedSQL *string, includePlan bool, clarification string) {
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))
	sc := stepContext{runID: run.ID, workspaceID: run.WorkspaceID, userID: userID}

	// The determinism-cache key folds the seed SQL and any clarification answer into one seed slot
	// (see tryCacheHit/rememberCache): identical question + schema + seed replay; anything different
	// executes fresh.
	cacheSeed := ""
	if seedSQL != nil {
		cacheSeed = *seedSQL
	}
	if c := clarificationCacheSeed(clarification); c != "" {
		cacheSeed = c
	}

	// Determinism cache fast-path (FEAT-027): if a deterministically-identical run already completed
	// (same question, same schema, and — for edited SQL — same seed), replay its persisted outputs
	// and finish WITHOUT waking the engine or asking the model. Caching is best-effort: any hash
	// failure or store error logs and falls through to a normal execution.
	if e.cache != nil {
		if e.tryCacheHit(ctx, sc, userID, run, cacheSeed, log) {
			return
		}
	}

	// Load the resume ledger before any step work: already-completed steps (from a prior interrupted
	// run, or this run's own cache reply) are replayed by EnsureStep, never re-executed.
	completed, err := e.engine.Load(ctx, run.ID)
	if err != nil {
		log.Error("loading completed steps", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}
	sc.ledger = completed

	// Waking is reported BEFORE Acquire blocks: screens/04-workspace-home.md and 08-run-viewer.md
	// specify that a question submitted while the engine is sleeping queues and shows "Waking
	// engine..." — the engine queued run announcing the wake it is about to cause.
	if err := e.setRunStatus(ctx, log, run.ID, domain.RunWaking); err != nil {
		log.Error("marking run waking", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	handle, err := e.provider.Acquire(ctx, run.WorkspaceID)
	if err != nil {
		log.Error("acquiring engine", slog.String("error", err.Error()))
		// engine.ErrEngineUnavailable is surfaced as its own honest, retryable message; anything
		// else is a generic failure. Both are specific enough for the user to act on.
		if errors.Is(err, engine.ErrEngineUnavailable) {
			e.failRun(ctx, log, run.ID, "The engine couldn't be started. Try again.")
		} else if ctx.Err() == context.Canceled {
			// A halt cancelled the wake (FEAT-024): settle to halted, never a generic failure.
			e.haltRun(ctx, log, run)
		} else {
			e.failRun(ctx, log, run.ID, "Something went wrong starting the engine. Try again.")
		}
		return
	}
	defer func() {
		if err := e.provider.Release(handle); err != nil {
			log.Error("releasing engine", slog.String("error", err.Error()))
		}
	}()

	if err := e.setRunStatus(ctx, log, run.ID, domain.RunRunning); err != nil {
		log.Error("marking run running", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// The generate_sql step's input differs by path: the plan output when the model plans it, the
	// empty string when the user's edited SQL is the plan (or when a resume continues past plan).
	// The rest of the pipeline is identical.
	var generateSQLOutput string
	var planJSON string
	if seedSQL != nil {
		// FEAT-018 seeded path: no plan step row and no model generate_sql call. The user's exact
		// edited SQL is persisted as the generate_sql step output, so the Run Viewer and the Report
		// show the query that actually ran.
		generateSQLOutput, err = e.execStep(ctx, sc, domain.StepGenerateSQL, "", log, func() (string, error) {
			return e.seedGenerateSQL(*seedSQL)
		})
		if err != nil {
			log.Error("generate_sql step failed", slog.String("error", err.Error()))
			e.stepFailed(ctx, log, run)
			return
		}
	} else {
		// plan — only when this execution owns a plan step. Resuming a seeded run (or a run
		// interrupted after generate_sql) must not fabricate a plan row that never existed.
		var plan planOutput
		if includePlan {
			planJSON, err = e.execStep(ctx, sc, domain.StepPlan, "", log, func() (string, error) {
				return e.runPlan(ctx, sc, run, log)
			})
			if err != nil {
				log.Error("plan step failed", slog.String("error", err.Error()))
				e.stepFailed(ctx, log, run)
				return
			}

			if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
				log.Error("parsing plan output", slog.String("error", err.Error()))
				e.stepFailed(ctx, log, run)
				return
			}

			// FEAT-033 (Honest Uncertainty): a question the plan judges ambiguous pauses the run
			// with a specific clarifying question (screen 12) instead of proceeding to generate_sql
			// on a guess — and instead of the Phase 2 behavior of failing with a plain message. The
			// run is settled to halted (the paused state); the clarify endpoint answers it and this
			// pipeline re-enters with the answer in the plan output. The answer guard is what lets a
			// resumed (already-clarified) run sail past this branch.
			if plan.awaitingClarification() {
				log.Info("run paused: awaiting clarification", slog.String("question", plan.ClarificationQuestion))
				if err := e.setRunStatus(ctx, log, run.ID, domain.RunHalted); err != nil {
					log.Error("marking run halted for clarification", slog.String("error", err.Error()))
				}
				return
			}

			// A genuinely unanswerable question (data missing, no column that records what was
			// asked) still fails the run with a plain-language reason rather than proceeding to
			// generate_sql on a guess — the distinction from an ambiguity is the plan's own call.
			if !plan.Answerable {
				reason := plan.Reason
				if strings.TrimSpace(reason) == "" {
					reason = "This question couldn't be answered from the available data."
				}
				log.Info("run failed: question not answerable", slog.String("reason", reason))
				e.stepFailed(ctx, log, run)
				return
			}
		}

		// generate_sql — if already completed (a resume after a crash), EnsureStep replays its output.
		generateSQLOutput, err = e.execStep(ctx, sc, domain.StepGenerateSQL, planJSON, log, func() (string, error) {
			return e.runGenerateSQL(ctx, sc, run, plan, log)
		})
		if err != nil {
			log.Error("generate_sql step failed", slog.String("error", err.Error()))
			e.stepFailed(ctx, log, run)
			return
		}
	}

	var genSQL domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(generateSQLOutput), &genSQL); err != nil {
		log.Error("parsing generate_sql output", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// static_check (FEAT-028): a pure, deterministic analysis of the generated SQL — no model call
	// (AC3), no engine query. Findings are persisted as the step's output and surfaced in the Run
	// Viewer BEFORE the execute results are shown (AC2), but they never block execution: the
	// analysis is advisory (see domain.StaticCheckWarning's doc). The step's input is the
	// generate_sql output — the exact SQL text about to run — mirroring PipelineOrder.
	if _, err := e.execStep(ctx, sc, domain.StepStaticCheck, generateSQLOutput, log, func() (string, error) {
		return e.runStaticCheck(ctx, sc, generateSQLOutput)
	}); err != nil {
		log.Error("static_check step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// execute
	var reconcile domain.ReconciliationSummary
	executeOutput, err := e.execStep(ctx, sc, domain.StepExecute, generateSQLOutput, log, func() (string, error) {
		return e.runExecute(ctx, sc, handle, genSQL.SQL, &reconcile, log)
	})
	if err != nil {
		log.Error("execute step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	var executeOut domain.ExecuteStepOutput
	if err := json.Unmarshal([]byte(executeOutput), &executeOut); err != nil {
		log.Error("parsing execute output", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// shadow_check (FEAT-031, Verification Layer 4): an independent, deterministic cross-check of
	// the aggregate's totals, recomputed directly over the source table — no extra model call (AC3).
	// A disagreement is persisted in the step output and surfaced in the Run Viewer; it never blocks
	// (the analysis is advisory, like static_check).
	if _, err := e.execStep(ctx, sc, domain.StepShadowCheck, executeOutput, log, func() (string, error) {
		return e.runShadowCheck(ctx, sc, handle, genSQL.SQL, executeOut, log)
	}); err != nil {
		log.Error("shadow_check step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// adversarial_review (FEAT-032, Verification Layer 5): the ONE verification layer with an extra
	// model call (AC3). A separate model pass sees only the question + SQL — never the results (AC1,
	// enforced by building the payload through the shared metadata-only seam) — and a detected
	// semantic mismatch blocks the run before chart_spec/narrate show any results (AC2).
	if _, err := e.execStep(ctx, sc, domain.StepAdversarialReview, generateSQLOutput, log, func() (string, error) {
		return e.runAdversarialReview(ctx, sc, run, genSQL.SQL, log)
	}); err != nil {
		log.Error("adversarial_review step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// chart_spec (FEAT-016) — the model emits a parsable ChartSpec over the execute facts only.
	chartSpecOutput, err := e.execStep(ctx, sc, domain.StepChartSpec, executeOutput, log, func() (string, error) {
		return e.runChartSpec(ctx, sc, run, executeOut, log)
	})
	if err != nil {
		log.Error("chart_spec step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}
	var chartOut domain.ChartSpecStepOutput
	if err := json.Unmarshal([]byte(chartSpecOutput), &chartOut); err != nil {
		log.Error("parsing chart_spec output", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// narrate (FEAT-015/FEAT-030) — plain-language prose over the execute facts only, never data
	// outside the result set, plus the model's structured claims mapping figures back to result
	// cells. The step's input is the execute output JSON, per features.md; architecture.md's
	// PipelineOrder places chart_spec before narrate.
	narrateOutput, err := e.execStep(ctx, sc, domain.StepNarrate, executeOutput, log, func() (string, error) {
		return e.runNarrate(ctx, sc, run, genSQL.SQL, reconcile, executeOut, log)
	})
	if err != nil {
		log.Error("narrate step failed", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}
	var narrateOut domain.NarrateStepOutput
	if err := json.Unmarshal([]byte(narrateOutput), &narrateOut); err != nil {
		log.Error("parsing narrate output", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// FEAT-030: resolve the model's proposed claims against the ACTUAL executed result set. A claim
	// bound only if it names a model-evident column and a real row (see buildProvenanceClaims) — a
	// number that cannot be traced to a cell is simply not back-linked, never presented as verified.
	var links []domain.ProvenanceLink
	if len(narrateOut.Claims) > 0 {
		built, err := e.buildProvenanceClaims(ctx, sc.workspaceID, genSQL.SQL, executeOut, narrateOut.Claims)
		if err != nil {
			log.Error("building provenance claims", slog.String("error", err.Error()))
			e.stepFailed(ctx, log, run)
			return
		}
		links = built
	}

	// Report materialization (FEAT-014's completion boundary): a run whose steps all succeeded but
	// whose Report could not be persisted is a corrupt state, so a failure here fails the run.
	if err := e.createReport(ctx, sc, run, reconcile, chartOut.ChartSpec, narrateOut.Narrative, links); err != nil {
		log.Error("creating report", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}

	// Determinism cache (FEAT-027): persist this successful run's outputs under a
	// determinism-identical key so a later identical run short-circuits model+engine entirely. The
	// run is already materially complete (report persisted), so a cache write failure is best-effort:
	// log it and complete anyway rather than failing a finished run on secondary bookkeeping.
	// cacheSeed folds in the clarification answer (see executePipeline), so a clarified run never
	// shadows an unclarified one.
	if e.cache != nil {
		if err := e.rememberCache(ctx, sc, run, cacheSeed, planJSON, generateSQLOutput, executeOutput, chartSpecOutput, narrateOutput, reconcile); err != nil {
			log.Warn("persisting run cache", slog.String("error", err.Error()))
		}
	}

	if err := e.setRunStatus(ctx, log, run.ID, domain.RunComplete); err != nil {
		log.Error("marking run complete", slog.String("error", err.Error()))
		e.stepFailed(ctx, log, run)
		return
	}
	log.Info("run complete")
}

// failRun marks a run failed. Variadic messages are logged (not persisted — a failed run's visible
// reason lives in the failed step's output); the transition is published to the live stream.
func (e *Executor) failRun(ctx context.Context, log *slog.Logger, runID string, messages ...string) {
	if err := e.setRunStatus(ctx, log, runID, domain.RunFailed); err != nil {
		log.Error("marking run failed", slog.String("error", err.Error()))
		return
	}
	if len(messages) > 0 {
		log.Info("run failed", slog.String("reason", messages[0]))
	}
}

// execStep runs one pipeline step THROUGH the orchestrator step machine (FEAT-022): if a completed
// step for (run, step) already exists, its output is replayed and work is NOT re-run; otherwise work
// is performed, the step claimed, and settled. The idempotency key is runID:stepName — deterministic
// per (run, step), which is exactly what makes retry+resume safe: re-entering the same step either
// replays the persisted output or collides on the table's UNIQUE constraint instead of running twice.
func (e *Executor) execStep(
	ctx context.Context,
	sc stepContext,
	stepName domain.StepName,
	input string,
	log *slog.Logger,
	work func() (string, error),
) (string, error) {
	output, _, err := e.engine.EnsureStep(ctx, sc.ledger, orchestrator.Call{
		RunID:          sc.runID,
		WorkspaceID:    sc.workspaceID,
		StepName:       stepName,
		Input:          input,
		IdempotencyKey: fmt.Sprintf("%s:%s", sc.runID, stepName),
	}, work)
	return output, err
}

// runPlan executes the plan step: build the workspace's payload, ask the model whether the question
// is answerable and how to approach it, and persist the typed PlanStepOutput.
// parseModelJSON unmarshals a model step response into v, tolerating the markdown fenced-code
// wrapper (```json ... ```) the Anthropic model occasionally wraps its JSON in despite the prompt.
// json.Unmarshal rejects the backticks outright — the "plan step returned unparseable output:
// invalid character '`' looking for beginning of value" failure seen in production. Stripping the
// fence before parsing is not leniency that hides bad output: the stored step output remains the
// parsed JSON (never the fence), and a body that is still invalid JSON after the fence is removed
// surfaces the same honest unparseable-output error as before.
func parseModelJSON(raw []byte, v any) error {
	b := bytes.TrimSpace(raw)
	if bytes.HasPrefix(b, []byte("```")) {
		lines := bytes.Split(b, []byte("\n"))
		// Drop the opening fence line (``` or ```json).
		lines = lines[1:]
		// Drop a trailing closing fence line if one is present.
		for n := len(lines); n > 0; n-- {
			last := bytes.TrimSpace(lines[n-1])
			if bytes.HasPrefix(last, []byte("```")) {
				lines = lines[:n-1]
				break
			}
		}
		b = bytes.TrimSpace(bytes.Join(lines, []byte("\n")))
	}
	return json.Unmarshal(b, v)
}

func (e *Executor) runPlan(ctx context.Context, sc stepContext, run domain.Run, log *slog.Logger) (string, error) {
	payload, err := e.buildPayload(ctx, sc.workspaceID, run.QuestionText, "")
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, planSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	// planOutput extends domain.PlanStepOutput with the FEAT-033 clarification fields; the model's
	// reply may carry either the original answerable/reason/approach contract or the new
	// needs_clarification surface (see planSystemPrompt).
	var out planOutput
	if err := parseModelJSON([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("plan step returned unparseable output: %w", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encoding plan output: %w", err)
	}
	return string(encoded), nil
}

// runGenerateSQL executes the generate_sql step: the same workspace payload, this time carrying the
// plan's chosen approach as prior context, answered with the SQL that execute will run.
//
// When the run was paused for a FEAT-033 clarifying question and the user answered it, the answer
// rides into the prior context too — generate_sql must honor the clarification, not re-derive the
// ambiguity it resolved.
func (e *Executor) runGenerateSQL(ctx context.Context, sc stepContext, run domain.Run, plan planOutput, log *slog.Logger) (string, error) {
	prior := plan.Approach
	if answer := plan.clarificationResolution(); answer != "" {
		question := strings.TrimSpace(plan.ClarificationQuestion)
		if question == "" {
			question = "the earlier ambiguity"
		}
		clause := fmt.Sprintf("The user clarified %s with: %s.", question, answer)
		if prior != "" {
			prior += " " + clause
		} else {
			prior = clause
		}
	}

	payload, err := e.buildPayload(ctx, sc.workspaceID, run.QuestionText, prior)
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, generateSQLSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var out domain.GenerateSQLStepOutput
	if err := parseModelJSON([]byte(raw), &out); err != nil {
		return "", fmt.Errorf("generate_sql step returned unparseable output: %w", err)
	}
	if strings.TrimSpace(out.SQL) == "" {
		return "", errors.New("generate_sql step returned no SQL")
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encoding generate_sql output: %w", err)
	}
	return string(encoded), nil
}

// seedGenerateSQL builds the generate_sql step output for FEAT-018's edited-SQL path: the user's
// SQL, verbatim (outer whitespace trimmed), wrapped in the same GenerateSQLStepOutput shape execute
// consumes — so the seeded run is indistinguishable downstream from one whose query the model wrote.
// A whitespace-only value is rejected here rather than persisted, so a run never executes nothing.
func (e *Executor) seedGenerateSQL(sql string) (string, error) {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return "", errors.New("edited SQL is empty")
	}
	encoded, err := json.Marshal(domain.GenerateSQLStepOutput{SQL: trimmed})
	if err != nil {
		return "", fmt.Errorf("encoding seeded generate_sql output: %w", err)
	}
	return string(encoded), nil
}

// runChartSpec executes the chart_spec step (FEAT-016): the model emits a parsable domain.ChartSpec
// over the execute step's facts — a chart type plus column-to-axis encodings, never a chart image or
// arbitrary markup. The spec is color-free by construction; the Wave 4 renderer assigns the branding
// chart tokens, and the prompt constrains encodings to be token-compatible.
func (e *Executor) runChartSpec(ctx context.Context, sc stepContext, run domain.Run, out domain.ExecuteStepOutput, log *slog.Logger) (string, error) {
	payload, err := e.buildFactsPayload(ctx, sc.workspaceID, run.QuestionText, "", out)
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, chartSpecSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var spec domain.ChartSpec
	if err := parseModelJSON([]byte(raw), &spec); err != nil {
		return "", fmt.Errorf("chart_spec step returned unparseable output: %w", err)
	}
	if !isValidChartType(spec.ChartType) {
		return "", fmt.Errorf("chart_spec step returned unsupported chart type %q", spec.ChartType)
	}

	encoded, err := json.Marshal(domain.ChartSpecStepOutput{ChartSpec: spec})
	if err != nil {
		return "", fmt.Errorf("encoding chart_spec output: %w", err)
	}
	return string(encoded), nil
}

// runNarrate executes the narrate step (FEAT-015/FEAT-030): the model writes plain-language prose
// explaining what the facts show, plus the structured claims mapping every figure back to a result
// cell. It narrates ONLY over the execute result set carried by the facts payload — never over data
// outside the result set, and never over withheld column values (BuildFactsPayload drops them before
// the payload is sealed).
//
// sourceSQL is the exact SQL that produced the result set — the model never sees it; it is carried
// separately so resolved claims can store it verbatim as their source query. reconcile carries
// FEAT-029's measured exclusion warnings: when a filter dropped a significant share of rows, the
// narrate prompt demands the model state the callout in prose, so the report "unprompted surfaces
// that fact" (features.md FEAT-029 AC2) rather than only showing it in a side widget.
func (e *Executor) runNarrate(ctx context.Context, sc stepContext, run domain.Run, sourceSQL string, reconcile domain.ReconciliationSummary, out domain.ExecuteStepOutput, log *slog.Logger) (string, error) {
	payload, err := e.buildFactsPayload(ctx, sc.workspaceID, run.QuestionText, "", out)
	if err != nil {
		return "", err
	}

	prompt := narrateSystemPrompt
	if len(reconcile.ExclusionWarnings) > 0 {
		// Reconciliation facts are aggregates, not rows — safe to put in the prompt (never the facts
		// payload seam, which is sealed). The model is told to surface them plainly so the narrative
		// carries the callout itself.
		prompt += "\n\nRECONCILIATION — the query that produced the result set measured these exclusions. State them plainly in the narrative where they matter; do not bury them and do not invent others:\n- " +
			strings.Join(reconcile.ExclusionWarnings, "\n- ")
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, prompt, payload)
	if err != nil {
		return "", err
	}

	var outNarrate domain.NarrateStepOutput
	if err := parseModelJSON([]byte(raw), &outNarrate); err != nil {
		return "", fmt.Errorf("narrate step returned unparseable output: %w", err)
	}
	if strings.TrimSpace(outNarrate.Narrative) == "" {
		return "", errors.New("narrate step returned no narrative")
	}

	encoded, err := json.Marshal(outNarrate)
	if err != nil {
		return "", fmt.Errorf("encoding narrate output: %w", err)
	}
	return string(encoded), nil
}

// isValidChartType reports whether t is one of the chart types the first-party renderer supports and
// the schema's enum allows.
func isValidChartType(t domain.ChartType) bool {
	switch t {
	case domain.ChartBar, domain.ChartLine, domain.ChartPie, domain.ChartScatter, domain.ChartTable:
		return true
	default:
		return false
	}
}

// runExecute executes the generated SQL against the workspace's engine and returns the typed
// ExecuteStepOutput — the facts FEAT-014 returns, converted to JSON scalars at this boundary. It
// also fills reconcile with the run's reconciliation measurements: rows_out is the executed result's
// row count (never fabricated), rows_in is the measured source-row total the SQL referenced (summed
// count(*) over the dataset views the SQL mentions), and an exclusion warning is recorded when
// rows_in could not be measured.
//
// The row-count ceiling (config.MaxExecuteRows) is enforced HERE, not at payload time: a query that
// returns more rows than the ceiling fails the execute step with an honest message, and the run
// with it — a silent truncation would corrupt every downstream number.
func (e *Executor) runExecute(ctx context.Context, sc stepContext, handle engine.Handle, sql string, reconcile *domain.ReconciliationSummary, log *slog.Logger) (string, error) {
	cleanups, err := e.prepareViews(ctx, handle, sc.workspaceID)
	if err != nil {
		return "", err
	}
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	rows, err := handle.Query(ctx, sql)
	if err != nil {
		return "", fmt.Errorf("executing query: %w", err)
	}
	defer rows.Close()

	out := domain.ExecuteStepOutput{Columns: rows.Columns()}
	for rows.Next() {
		raw := make([]any, len(out.Columns))
		dest := make([]any, len(out.Columns))
		for i := range dest {
			dest[i] = &raw[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return "", fmt.Errorf("scanning row: %w", err)
		}
		converted := make([]any, len(out.Columns))
		for i, v := range raw {
			converted[i] = convertValue(v)
		}
		out.Rows = append(out.Rows, converted)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("iterating query rows: %w", err)
	}
	out.RowCount = len(out.Rows)

	if out.RowCount > config.MaxExecuteRows {
		return "", fmt.Errorf(
			"This query returned %d rows, more than the %d-row limit per answer. Refine the question to return fewer rows — for example by adding a filter or using an aggregate — and try again.",
			out.RowCount, config.MaxExecuteRows)
	}

	// Reconciliation rows_in is measured at execute time, while the views the SQL references are
	// still attached: count(*) over each known dataset view the SQL mentions, summed.
	rowsIn, warnings, err := e.measureRowsIn(ctx, handle, sql, sc.workspaceID)
	if err != nil {
		return "", err
	}

	// FEAT-029: on top of the measure failure warnings, surface data-grounded exclusion warnings —
	// a filter discarding a large share of measured rows, or a filter over a column the profiler
	// proves is materially null. Pure computation over measured numbers; no extra engine query.
	nullRates, err := e.nullRateByColumn(ctx, sc.workspaceID)
	if err != nil {
		return "", fmt.Errorf("reading column null rates for reconciliation: %w", err)
	}
	warnings = append(warnings, reconcileExclusionWarnings(sql, rowsIn, int64(out.RowCount), nullRates)...)

	reconcile.RowsIn = rowsIn
	reconcile.RowsOut = int64(out.RowCount)
	reconcile.ExclusionWarnings = warnings

	// Query executed — the workspace-scoped action is logged by the code that performed it, in the
	// same code path (audit's one rule).
	if err := e.audit.WriteQueryExecuted(ctx, sc.workspaceID, sc.userID); err != nil {
		return "", fmt.Errorf("recording query execution: %w", err)
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return "", fmt.Errorf("encoding execute output: %w", err)
	}
	return string(encoded), nil
}

// measureRowsIn computes the number of source rows the reconciliation summary's rows_in should
// report for SQL: it scans the SQL for each known dataset view name (tableNameForDataset, the same
// name prepareViews attaches and the model is taught) and sums count(*) over every view it
// references.
//
// If no known view name can be matched — which should not happen for valid generated SQL but could
// for hand-crafted SQL — rows_in is set to 0 AND an explicit exclusion warning is recorded, so a
// fabricated-looking number is never silently reported.
func (e *Executor) measureRowsIn(ctx context.Context, handle engine.Handle, sql string, workspaceID string) (rowsIn int64, warnings []string, err error) {
	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return 0, nil, fmt.Errorf("listing datasets for row measurement: %w", err)
	}
	known := make([]string, 0, len(datasets))
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		known = append(known, tableNameForDataset(ds))
	}

	lowerSQL := strings.ToLower(sql)
	var matched int
	for _, name := range known {
		if !containsSQLIdentifier(lowerSQL, name) {
			continue
		}
		n, err := e.countRowsInView(ctx, handle, name)
		if err != nil {
			return 0, nil, err
		}
		rowsIn += n
		matched++
	}

	if matched == 0 {
		warnings = append(warnings,
			"This query's input row count couldn't be measured — it didn't reference a recognized dataset view, so rows_in is reported as 0.")
	}
	return rowsIn, warnings, nil
}

// countRowsInView runs count(*) over a single engine view and returns the integer total.
func (e *Executor) countRowsInView(ctx context.Context, handle engine.Handle, view string) (int64, error) {
	// view is a sanitized identifier (letters/digits/underscore) built by tableNameForDataset, so it
	// is safe to embed unquoted in the SELECT.
	rows, err := handle.Query(ctx, "SELECT count(*) FROM "+view)
	if err != nil {
		return 0, fmt.Errorf("counting rows in %s: %w", view, err)
	}
	defer rows.Close()

	var total int64
	for rows.Next() {
		var raw any
		if err := rows.Scan(&raw); err != nil {
			return 0, fmt.Errorf("scanning row count for %s: %w", view, err)
		}
		total += countValue(raw)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterating row count for %s: %w", view, err)
	}
	return total, nil
}

// countValue converts a scanned scalar to an int64 for a count. DuckDB's driver may surface a count
// as int64, int32, or (for some result typing) float64; all are accepted so a runtime variance never
// misreports rows_in.
func countValue(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case float32:
		return int64(t)
	case float64:
		return int64(t)
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n
		}
	}
	return 0
}

// containsSQLIdentifier reports whether needle (a lowercased dataset view name) appears in haystack
// as a standalone SQL identifier — not as a suffix or prefix of a longer identifier, which keeps a
// string constant or a longer name from being miscounted as a reference.
func containsSQLIdentifier(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	start := 0
	for {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			return false
		}
		pos := start + i
		beforeOK := pos == 0 || !isIdentifierByte(haystack[pos-1])
		after := pos + len(needle)
		afterOK := after == len(haystack) || !isIdentifierByte(haystack[after])
		if beforeOK && afterOK {
			return true
		}
		start = pos + len(needle)
	}
}

// isIdentifierByte reports whether b is a character that can continue a bare SQL identifier.
func isIdentifierByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// createReport materializes the run's Report at completion (FEAT-014's assembly boundary). The
// Report is fully self-contained: title derived from the question, the narrate step's narrative, the
// chart_spec step's spec, the measured reconciliation summary, and (FEAT-030) the resolved
// provenance links. A run whose steps all succeeded but whose Report cannot be persisted is a
// corrupt state, so this fails the run rather than being best-effort.
//
// FEAT-021/FEAT-025: when the run was forked (run.ParentRunID set — a compositional follow-up or a
// fork-on-amendment), the new report records its parent report — the report the parent run
// materialized — so report history reads as a traceable tree (screens/09's Parent report link). The
// link is honest: if the parent run has no materialized report yet, the field stays empty rather
// than being fabricated.
func (e *Executor) createReport(ctx context.Context, sc stepContext, run domain.Run, reconcile domain.ReconciliationSummary, chart domain.ChartSpec, narrative string, links []domain.ProvenanceLink) error {
	parentReportID := ""
	if run.ParentRunID != "" {
		if parent, err := e.reports.GetByRunID(ctx, sc.workspaceID, run.ParentRunID); err == nil {
			parentReportID = parent.ID
		} else {
			e.logger.Warn("creating report: parent run has no materialized report",
				slog.String("parent_run_id", run.ParentRunID), slog.String("error", err.Error()))
		}
	}

	ids := make([]string, len(links))
	for i := range links {
		ids[i] = links[i].ID
	}
	report := domain.Report{
		ID:                    uuid.NewString(),
		WorkspaceID:           sc.workspaceID,
		RunID:                 sc.runID,
		Title:                 deriveReportTitle(run.QuestionText),
		Narrative:             narrative,
		ChartSpec:             chart,
		ProvenanceLinks:       ids,
		ReconciliationSummary: reconcile,
		ParentReportID:        parentReportID,
		CreatedAt:             time.Now().UTC(),
	}
	if err := e.reports.Create(ctx, report); err != nil {
		return fmt.Errorf("persisting report: %w", err)
	}

	// FEAT-030: persist the resolved provenance links (report-scoped rows). A Report whose claims
	// cannot be persisted is as corrupt as one whose narrative cannot — the links are the report's
	// verification surface — so a write failure here fails the run too. When no provenance store is
	// wired (tests/dev), the links simply stay in the report's ids array without backing rows.
	if len(links) > 0 && e.provenance != nil {
		for i := range links {
			links[i].ReportID = report.ID
		}
		if err := e.provenance.WriteLinks(ctx, sc.workspaceID, links); err != nil {
			return fmt.Errorf("persisting provenance links: %w", err)
		}
	}
	return nil
}

// reportTitleMax is the plain character ceiling a derived report title is truncated to. No ellipsis
// gymnastics — schema.md calls for "a short, human-readable title", and simple truncation is all
// that adds.
const reportTitleMax = 80

// deriveReportTitle turns the question text into a short report title by collapsing whitespace and
// truncating to the length bounded by reportTitleMax.
func deriveReportTitle(question string) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(question)), " ")
	if len(title) > reportTitleMax {
		title = title[:reportTitleMax]
	}
	return title
}

// buildPayload assembles the single shared model payload for a metadata-only step (plan,
func (e *Executor) buildPayload(ctx context.Context, workspaceID, question, priorContext string) (model.Payload, error) {
	grounding, err := semantic.AssembleGroundingContext(ctx, workspaceID, e.relationships, e.metrics, e.corrections)
	if err != nil {
		return model.Payload{}, err
	}

	tables, err := e.assembleTables(ctx, workspaceID)
	if err != nil {
		return model.Payload{}, err
	}

	return model.BuildPayload(question, priorContext, tables, grounding), nil
}

// buildFactsPayload assembles the facts payload for the narrate and chart_spec steps: the SAME
// metadata every step sees, plus the executed result set — filtered by model.BuildFactsPayload so
// any PII-flagged or excluded column's VALUES never enter the payload. This is the R-016 seam for
// facts-consuming steps: narrate/chart_spec never construct a payload by hand.
func (e *Executor) buildFactsPayload(ctx context.Context, workspaceID, question, priorContext string, out domain.ExecuteStepOutput) (model.Payload, error) {
	grounding, err := semantic.AssembleGroundingContext(ctx, workspaceID, e.relationships, e.metrics, e.corrections)
	if err != nil {
		return model.Payload{}, err
	}

	tables, err := e.assembleTables(ctx, workspaceID)
	if err != nil {
		return model.Payload{}, err
	}

	return model.BuildFactsPayload(question, priorContext, tables, grounding, model.Facts{
		Columns: out.Columns,
		Rows:    out.Rows,
	}), nil
}

// assembleTables builds the TableMetadata list for a workspace's complete datasets, mirroring the
// metadata surface the payload seam consumes (see buildPayload).
func (e *Executor) assembleTables(ctx context.Context, workspaceID string) ([]model.TableMetadata, error) {
	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing datasets: %w", err)
	}

	tables := make([]model.TableMetadata, 0, len(datasets))
	for _, ds := range datasets {
		// Only complete datasets are queryable; a file still ingesting has no engine view and its
		// schema is provisional anyway.
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		cols, err := e.columns.ListByDataset(ctx, ds.ID)
		if err != nil {
			return nil, fmt.Errorf("listing columns for %s: %w", ds.ID, err)
		}
		tm := model.TableMetadata{
			DatasetID: ds.ID,
			Name:      tableNameForDataset(ds),
			Columns:   make([]model.ColumnMetadata, 0, len(cols)),
		}
		for _, c := range cols {
			tm.Columns = append(tm.Columns, model.ColumnMetadata{
				Name:              c.Name,
				Type:              string(c.InferredType),
				PIIFlag:           c.PIIFlag,
				ExcludedFromModel: c.ExcludedFromModel,
				NullRate:          c.NullRate,
				DistinctCount:     c.DistinctCount,
			})
		}
		tables = append(tables, tm)
	}

	if len(tables) == 0 {
		return nil, errors.New("workspace has no complete datasets to query")
	}
	return tables, nil
}

// prepareViews makes every complete dataset queryable inside the workspace's engine session by
// downloading its normalized Parquet to a temp file the session can read and attaching a view over
// it under the dataset's canonical table name (tableNameForDataset).
//
// Production architecture.md describes DuckDB attaching views over R2 via httpfs; the engine seam
// exposed to this package offers no object-store reader, so this wave mirrors ingest's own
// download-to-temp pattern (ingest's downloadToTemp is unexported; reported as a shared-module
// consideration). CREATE OR REPLACE makes re-runs idempotent. Each returned cleanup removes the
// temp file; callers must run them after the query completes.
func (e *Executor) prepareViews(ctx context.Context, handle engine.Handle, workspaceID string) (cleanups []func(), err error) {
	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("listing datasets for views: %w", err)
	}
	return PrepareViews(ctx, handle, workspaceID, datasets, e.objects)
}

// PrepareViews is the exported form of the executor's view-attachment step: download every complete
// dataset's normalized Parquet to a temp file and CREATE OR REPLACE VIEW it under the canonical
// table name, so any engine-handle consumer outside a run (FEAT-051's smart-generation profiling)
// queries the same views the executor teaches the model.
//
// Views are session-scoped and reference the downloaded temp files, so callers MUST run the
// returned cleanups once profiling is done. CREATE OR REPLACE makes re-attachment idempotent.
func PrepareViews(ctx context.Context, handle engine.Handle, workspaceID string, datasets []domain.Dataset, objects ObjectStore) (cleanups []func(), err error) {
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		path, cleanup, err := downloadToTemp(ctx, objects, storage.NormalizedDatasetKey(workspaceID, ds.ID), ".parquet")
		if err != nil {
			// Run the cleanups accumulated so far so a partial failure does not litter temp files.
			for _, done := range cleanups {
				done()
			}
			return nil, err
		}
		cleanups = append(cleanups, cleanup)

		name := tableNameForDataset(ds)
		// name is sanitized (letters/digits/underscore), so it is safe to embed in the DDL; the
		// path goes through a SQL string literal, so quotes are doubled.
		escapedPath := strings.ReplaceAll(path, "'", "''")
		if _, err := handle.Exec(ctx,
			"CREATE OR REPLACE VIEW "+name+" AS SELECT * FROM read_parquet('"+escapedPath+"')"); err != nil {
			for _, done := range cleanups {
				done()
			}
			return nil, fmt.Errorf("creating engine view %s: %w", name, err)
		}
	}
	return cleanups, nil
}

// downloadToTemp copies object key into a fresh temp file and returns its path and a cleanup func
// that closes and removes it. Mirrors the ingest pipeline's private helper (see prepareViews's doc).
func downloadToTemp(ctx context.Context, objects ObjectStore, key, ext string) (string, func(), error) {
	reader, err := objects.GetObject(ctx, key)
	if err != nil {
		return "", nil, fmt.Errorf("downloading %s: %w", key, err)
	}
	defer reader.Close()

	file, err := os.CreateTemp("", "groundwork-run-*"+ext)
	if err != nil {
		return "", nil, fmt.Errorf("creating temp file: %w", err)
	}
	cleanup := func() {
		file.Close()
		os.Remove(file.Name())
	}

	if _, err := io.Copy(file, reader); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("writing temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(file.Name())
		return "", nil, fmt.Errorf("closing temp file: %w", err)
	}
	return file.Name(), cleanup, nil
}

// TableNameForDataset is the exported form of tableNameForDataset, for callers outside internal/run
// that must query the same engine views the executor uses (FEAT-051's smart-generation profiling
// queries the dataset views by this exact name).
func TableNameForDataset(ds domain.Dataset) string {
	return tableNameForDataset(ds)
}

// tableNameForDataset returns the canonical SQL identifier a dataset's data is exposed under in the
// engine, and taught to the model via model.TableMetadata.Name — the "internal/run's table-naming
// helper" payload.go's TableMetadata doc references.
//
// Convention: "dataset_" + sanitized original-filename stem (max 40 chars) + "_" + first 8
// alphanumeric characters of the dataset id. The filename makes generated SQL readable
// (sales_data_a1b2c3d4 beats dataset_a1b2c3d4); the id suffix makes it collision-proof even for
// FEAT-049's "Keep both" duplicates; the sanitizer keeps it a legal unquoted SQL identifier.
func tableNameForDataset(ds domain.Dataset) string {
	stem := sanitizeIdentifier(ds.OriginalFilename)
	if stem == "" {
		stem = "dataset"
	}
	if len(stem) > 40 {
		stem = stem[:40]
	}

	idSuffix := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		return -1
	}, strings.ToLower(ds.ID))
	if len(idSuffix) > 8 {
		idSuffix = idSuffix[:8]
	}
	if idSuffix == "" {
		idSuffix = "data"
	}

	return "dataset_" + stem + "_" + idSuffix
}

// sanitizeIdentifier reduces s to lowercase letters, digits, and underscores, collapsing other runs
// into single underscores, so the result is a legal unquoted SQL identifier.
func sanitizeIdentifier(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevUnderscore := true
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteRune('_')
				prevUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

// convertValue maps a DuckDB-native query value to the JSON scalar domain/run.go's ExecuteStepOutput
// promises (string, float64, bool, or nil). Anything the driver returns that has no direct JSON
// scalar form (e.g. decimal, enum, date) is rendered as its string form so a result is never
// partially lossy.
func convertValue(v any) any {
	switch t := v.(type) {
	case nil, string, float64, bool:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case float32:
		return float64(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// planSystemPrompt frames the plan step: decide answerability against the payload's actual schema
// and semantic layer, and never guess. The model's reply must be JSON matching planOutput — the
// answerable contract (FEAT-013 AC4) plus the FEAT-033 clarification surface.
//
// The distinction the prompt enforces is the whole of Honest Uncertainty: an AMBIGUOUS question
// (the data could answer it, but which metric/period/grain is unclear) pauses for a specific
// clarifying question; a genuinely UNANSWERABLE question (the data simply lacks what was asked)
// fails with a plain-language reason. A clarification previously given in this workspace rides in
// the payload's corrections (persisted by the clarify endpoint) and must NOT be re-asked.
const planSystemPrompt = `You are the planning step of a grounded analytics system. You will be given a user's question and the schema/profile metadata of the datasets available in their workspace, plus any confirmed relationships, metric definitions, and corrections that form the workspace's semantic layer.

Your job is to decide, from the schema and semantic layer alone, whether the question can be answered against this workspace's data — NOT to answer it yourself, and NEVER to invent data or make up values. There is no code path where you answer from data held in the prompt: only the schema and metadata are here.

Respond with ONLY a JSON object of the form:
{"answerable": true|false, "reason": "...", "approach": "...", "needs_clarification": true|false, "clarification_question": "...", "clarification_options": ["..."], "ambiguity_key": "..."}

Decide in this order:
1. If the question is genuinely AMBIGUOUS — the data could answer it, but a needed choice is undefined (which metric, over what time period, at what grain, which statuses count) — set needs_clarification to true and answerable to true, and:
   - clarification_question must name the SPECIFIC ambiguity and ask for the exact resolution, e.g. "Do you mean revenue by total value or by order count?" or "Over what time period?" — never a generic 'please clarify'.
   - clarification_options should list a small enumerable set of discrete resolutions when one exists (e.g. ["By revenue", "By count"]); omit it when only a free-text answer makes sense.
   - ambiguity_key should be a stable short key naming the ambiguity, e.g. "metric_undefined" or "time_period_undefined".
   - Omit approach; the approach is decided once the user resolves the ambiguity.
2. If the question is unanswerable because the DATA genuinely lacks what it asks (e.g. "The data has no column that records region.") — set answerable to false, reason to a short plain-language explanation, and omit approach. Do not ask a clarifying question for a missing-column gap; no answer to a clarification would create data that does not exist.
3. If the question is clear and answerable — set answerable to true, omit needs_clarification, and set approach to a concise plan for the SQL step: which tables, joins, and metrics it should use.

The payload may carry a corrections array. A correction whose description begins with "Clarification:" records an ambiguity already resolved in this workspace: if its bracketed key (e.g. "[metric_undefined]") matches the ambiguity you would otherwise raise, do NOT re-ask — treat the ambiguity as resolved using that correction's corrected_logic, set answerable to true, and proceed with an approach.
Do not include markdown fences or any text outside the JSON object.`

// generateSQLSystemPrompt frames the generate_sql step: write SQL referencing exactly the table
// names and columns provided, following the plan's approach.
const generateSQLSystemPrompt = `You are the SQL generation step of a grounded analytics system. The user's question and a plan are provided alongside the schema/profile metadata of the workspace's datasets and its semantic layer.

Write read-only analytical SQL that answers the question. Requirements:
- Reference tables by EXACTLY the names given in the payload. Never invent table or column names.
- Prefer the confirmed relationships and metric definitions in the semantic layer for joins and calculations.
- Only join tables when a confirmed or clearly implied relationship connects them.
- Return only the columns the question needs. Use aggregate functions and GROUP BY for summarization questions.
- Use standard SQL; the engine is DuckDB (functions like date_trunc, strftime, unnest are available).
- Never return row-level values for columns that are withheld (they will not be listed in the payload — do not try to select them).

Respond with ONLY a JSON object of the form {"sql": "..."} containing the SQL as a string. Escape any embedded double quotes. Do not include markdown fences or any text outside the JSON object.`

// chartSpecSystemPrompt frames the chart_spec step (FEAT-016): the model receives the executed
// result set (the facts) and must emit a parsable ChartSpec — never a chart image or freeform
// markup. The spec is color-free BY CONTRACT: the first-party renderer assigns the branding chart
// tokens (chart-cat-1..8 / chart-seq / chart-div from branding.md) in Wave 4, so the model must
// choose token-compatible encodings now — a categorical series split for the categorical tokens, a
// single magnitude column for the sequential ramp, and above/below-baseline directionality for the
// diverging tokens (red/green-coded meaning must only ever be used for directional values).
//
// A chart only earns its place when it adds clarity; otherwise the spec MUST be a table.
const chartSpecSystemPrompt = `You are the chart specification step of a grounded analytics system. You are given the executed result set of a user's question: its column names and the actual data rows.

Your job is to decide whether a chart would make this result set clearer, and if so emit a structured specification for the first-party renderer to draw. You NEVER produce an image, SVG, HTML, or any markup — only a JSON specification object.

Respond with ONLY a JSON object of the form:
{"chart_type": "...", "x_axis_column": "...", "y_axis_column": "...", "series_column": "...", "title": "..."}

Rules:
- chart_type must be exactly one of: "bar", "line", "pie", "scatter", "table".
- When a chart does not add clarity (few rows, one value, purely tabular detail, or when the question is answered best by reading the numbers), chart_type MUST be "table". Every result set still gets a spec.
- x_axis_column and y_axis_column must be actual column names from the result set. For a "table" spec, set them to the most relevant columns (or empty strings if not applicable); title still describes the table.
- series_column splits the data into multiple series. It may be empty (single series). NEVER invent a series column that is not present in the result set.
- The spec carries NO colors. The renderer assigns the product's chart tokens, so choose encodings that stay token-compatible: use series_column for genuinely categorical groupings (the categorical token family has 8 fixed hues, assigned in fixed order), and for values that are directional relative to a baseline (above/below budget, period-over-period change) prefer a bar/line with the diverging token family in mind. Never encode direction as two unrelated categorical series.
- title is a short, human-readable chart title that does not restate the entire question.
Use ONLY column names exactly as they appear in the result set. Do not include markdown fences or any text outside the JSON object.`

// narrateSystemPrompt frames the narrate step (FEAT-015/FEAT-030): the model receives ONLY the
// executed result set (the facts) plus the workspace's schema metadata — it narrates over the facts
// and nothing else. Every stated number must be traceable to the result set, and the prose must
// stand alone for someone who never sees the SQL or schema.
//
// Claims (FEAT-030): the model additionally maps each stated figure back to the exact result row and
// column that produced it, so the product can make the figure clickable and verifiable. Row indexes
// are 0-based positions within the "rows" arrays in the payload. A number stated WITHOUT a claim is
// not back-linked; a number that gets a claim is only persisted if the executor can bind it to a
// real cell (the deterministic validator re-checks everything).
const narrateSystemPrompt = `You are the narrative step of a grounded analytics system. You are given the executed result set of a user's question: the actual data rows the query returned, alongside the workspace's schema metadata.

Your job is to write a clear, plain-language answer to the user's question that explains what the data shows. The result set is the ONLY data you may reference — never invent, estimate, or reach for numbers that are not present in the rows you were given.

Rules:
- Write prose a non-technical reader can act on. No SQL, no column names as jargon, no schema terminology.
- Every number you state must come from the provided result set, and each one should be stated so a reader could trace it back to a specific value.
- If the result set contains values that a human should treat carefully (for example a null in a key cell or an obvious outlier), mention it plainly rather than glossing over it.
- Keep the narrative focused and proportionate to the question. Do not pad.
- Do not restate the raw table row by row; summarize, compare, and call out what matters.

CLAIM MAPPING (verification mechanism):
- For EVERY number you state in the narrative, add one claim to the "claims" array that names the exact result-set cell the number came from: claim_text is the exact narrative span containing the number, column is the result column name (exactly as in the "columns" array), row_index is the 0-based position of the row in the "rows" array that holds the cell.
- Every stated number gets a claim; a number without a claim cannot be verified and will not be linkable. State only numbers you can map to a cell.

Respond with ONLY a JSON object of the form {"narrative": "...", "claims": [{"claim_text": "...", "column": "...", "row_index": 0}]}. "claims" may be an empty array. Escape any embedded double quotes. Do not include markdown fences or any text outside the JSON object.`

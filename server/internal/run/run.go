// Package run implements FEAT-013's linear persisted executor: the component that takes a created
// run and advances it — engine wake, plan, generate_sql, execute, chart_spec, narrate, then Report
// materialization — writing every boundary to real runs/steps rows as it goes.
//
// This is the Phase 2 boundary in concrete form (IMPLEMENTATION.md "Phase 2 boundary assumptions"):
// a LINEAR executor with no leases, heartbeats, forks, or resume. A step is created the moment its
// work begins and completed or failed in a single follow-up write; the per-step idempotency key
// (runID:stepName) is persisted so the schema's retry-safety guarantee is present from day one, but
// no retry machinery runs this wave. static_check, reconcile, shadow_check, adversarial_review, and
// assemble are Phase 3/5 and deliberately not implemented here.
//
// This package is the ONLY route to the model that exists at runtime: the executor builds every
// metadata payload through model.BuildPayload and every facts payload through model.BuildFactsPayload
// (the two shared construction functions risk_log.md R-016 requires — see model/payload.go's package
// doc for why the type system enforces them) and transmits them through model.Client, which writes
// the egress log in the same code path as the outbound call (behavior rule 7).
package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
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

// StepStore persists step rows. Satisfied by postgres.StepRepository.
type StepStore interface {
	Create(ctx context.Context, workspaceID string, step domain.Step) error
	Complete(ctx context.Context, id, output string) error
	Fail(ctx context.Context, id, reason string) error
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
type ReportStore interface {
	Create(ctx context.Context, report domain.Report) error
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

	// Semantic-layer listers feed semantic.AssembleGroundingContext (FEAT-019's building block).
	// Typed as the semantic package's own interfaces because that is exactly the contract
	// AssembleGroundingContext consumes; the same postgres repositories satisfy them.
	relationships semantic.RelationshipLister
	metrics       semantic.MetricDefinitionLister
	corrections   semantic.CorrectionLister
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
) *Executor {
	return &Executor{
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
	}
}

// Submit starts run's background execution.
//
// It returns immediately: runs are server-side state (architecture.md), so execution continues even
// if the submitting user navigates away. userID is the run's initiating user, needed for the
// workspace-scoped audit entries (domain.Run carries no user field).
func (e *Executor) Submit(userID string, run domain.Run) {
	go e.execute(context.Background(), userID, run)
}

// stepContext carries the identity fields every step write needs.
type stepContext struct {
	runID       string
	workspaceID string
	userID      string
}

// execute advances run through its pipeline: wake the engine, plan, generate_sql, execute, then a
// single terminal status. Every branch that cannot continue marks the run failed before returning.
func (e *Executor) execute(ctx context.Context, userID string, run domain.Run) {
	log := e.logger.With(slog.String("run_id", run.ID), slog.String("workspace_id", run.WorkspaceID))
	sc := stepContext{runID: run.ID, workspaceID: run.WorkspaceID, userID: userID}

	// Waking is reported BEFORE Acquire blocks: screens/04-workspace-home.md and 08-run-viewer.md
	// specify that a question submitted while the engine is sleeping queues and shows "Waking
	// engine..." — the queued run announcing the wake it is about to cause.
	if err := e.runs.UpdateStatus(ctx, run.ID, domain.RunWaking); err != nil {
		log.Error("marking run waking", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	handle, err := e.provider.Acquire(ctx, run.WorkspaceID)
	if err != nil {
		log.Error("acquiring engine", slog.String("error", err.Error()))
		// engine.ErrEngineUnavailable is surfaced as its own honest, retryable message; anything
		// else is a generic failure. Both are specific enough for the user to act on.
		if errors.Is(err, engine.ErrEngineUnavailable) {
			e.failRun(ctx, log, run.ID, "The engine couldn't be started. Try again.")
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

	if err := e.runs.UpdateStatus(ctx, run.ID, domain.RunRunning); err != nil {
		log.Error("marking run running", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// plan
	planOutput, err := e.execStep(ctx, sc, domain.StepPlan, "", log, func() (string, error) {
		return e.runPlan(ctx, sc, run, log)
	})
	if err != nil {
		log.Error("plan step failed", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	var plan domain.PlanStepOutput
	if err := json.Unmarshal([]byte(planOutput), &plan); err != nil {
		log.Error("parsing plan output", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// FEAT-033 boundary (Phase 2 form): an ambiguous or unanswerable question fails the run with a
	// plain-language reason rather than proceeding to generate_sql on a guess. The reason lives in
	// the completed plan step's output, where the Run Viewer reads it verbatim.
	if !plan.Answerable {
		reason := plan.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "This question couldn't be answered from the available data."
		}
		log.Info("run failed: question not answerable", slog.String("reason", reason))
		e.failRun(ctx, log, run.ID)
		return
	}

	// generate_sql
	generateSQLOutput, err := e.execStep(ctx, sc, domain.StepGenerateSQL, planOutput, log, func() (string, error) {
		return e.runGenerateSQL(ctx, sc, run, plan, log)
	})
	if err != nil {
		log.Error("generate_sql step failed", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	var genSQL domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(generateSQLOutput), &genSQL); err != nil {
		log.Error("parsing generate_sql output", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// execute
	var reconcile domain.ReconciliationSummary
	executeOutput, err := e.execStep(ctx, sc, domain.StepExecute, generateSQLOutput, log, func() (string, error) {
		return e.runExecute(ctx, sc, handle, genSQL.SQL, &reconcile, log)
	})
	if err != nil {
		log.Error("execute step failed", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	var executeOut domain.ExecuteStepOutput
	if err := json.Unmarshal([]byte(executeOutput), &executeOut); err != nil {
		log.Error("parsing execute output", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// chart_spec (FEAT-016) — the model emits a parsable ChartSpec over the execute facts only.
	chartSpecOutput, err := e.execStep(ctx, sc, domain.StepChartSpec, executeOutput, log, func() (string, error) {
		return e.runChartSpec(ctx, sc, run, executeOut, log)
	})
	if err != nil {
		log.Error("chart_spec step failed", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}
	var chartOut domain.ChartSpecStepOutput
	if err := json.Unmarshal([]byte(chartSpecOutput), &chartOut); err != nil {
		log.Error("parsing chart_spec output", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// narrate (FEAT-015) — plain-language prose over the execute facts only, never data outside the
	// result set. The step's input is the execute output JSON, per features.md; architecture.md's
	// PipelineOrder places chart_spec before narrate.
	narrateOutput, err := e.execStep(ctx, sc, domain.StepNarrate, executeOutput, log, func() (string, error) {
		return e.runNarrate(ctx, sc, run, executeOut, log)
	})
	if err != nil {
		log.Error("narrate step failed", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}
	var narrateOut domain.NarrateStepOutput
	if err := json.Unmarshal([]byte(narrateOutput), &narrateOut); err != nil {
		log.Error("parsing narrate output", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	// Report materialization (FEAT-014's completion boundary): a run whose steps all succeeded but
	// whose Report could not be persisted is a corrupt state, so a failure here fails the run.
	if err := e.createReport(ctx, sc, run, reconcile, chartOut.ChartSpec, narrateOut.Narrative); err != nil {
		log.Error("creating report", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}

	if err := e.runs.UpdateStatus(ctx, run.ID, domain.RunComplete); err != nil {
		log.Error("marking run complete", slog.String("error", err.Error()))
		e.failRun(ctx, log, run.ID)
		return
	}
	log.Info("run complete")
}

// failRun marks a run failed. Variadic messages are logged (not persisted — a failed run's visible
// reason lives in the failed step's output); failRun with no message logs only the transition.
func (e *Executor) failRun(ctx context.Context, log *slog.Logger, runID string, messages ...string) {
	if err := e.runs.UpdateStatus(ctx, runID, domain.RunFailed); err != nil {
		log.Error("marking run failed", slog.String("error", err.Error()))
		return
	}
	if len(messages) > 0 {
		log.Info("run failed", slog.String("reason", messages[0]))
	}
}

// execStep wraps one pipeline step: persist the step row (running), run work, then persist either
// its completed output or its failure. The returned string is the JSON-encoded output on success.
//
// The idempotency key is runID:stepName — deterministic per (run, step), which is exactly what makes
// retry safe in a later wave: re-executing the same step on the same run would collide on the
// table's UNIQUE constraint instead of running twice.
func (e *Executor) execStep(
	ctx context.Context,
	sc stepContext,
	stepName domain.StepName,
	input string,
	log *slog.Logger,
	work func() (string, error),
) (string, error) {
	now := time.Now().UTC()
	step := domain.Step{
		ID:             uuid.NewString(),
		RunID:          sc.runID,
		StepName:       stepName,
		Status:         domain.StepRunning,
		Input:          input,
		IdempotencyKey: fmt.Sprintf("%s:%s", sc.runID, stepName),
		StartedAt:      &now,
	}
	if err := e.steps.Create(ctx, sc.workspaceID, step); err != nil {
		return "", fmt.Errorf("persisting %s step: %w", stepName, err)
	}

	output, err := work()
	if err != nil {
		if failErr := e.steps.Fail(ctx, step.ID, err.Error()); failErr != nil {
			log.Error("persisting step failure", slog.String("step", string(stepName)), slog.String("error", failErr.Error()))
		}
		return "", err
	}

	if err := e.steps.Complete(ctx, step.ID, output); err != nil {
		return "", fmt.Errorf("persisting %s output: %w", stepName, err)
	}
	return output, nil
}

// runPlan executes the plan step: build the workspace's payload, ask the model whether the question
// is answerable and how to approach it, and persist the typed PlanStepOutput.
func (e *Executor) runPlan(ctx context.Context, sc stepContext, run domain.Run, log *slog.Logger) (string, error) {
	payload, err := e.buildPayload(ctx, sc.workspaceID, run.QuestionText, "")
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, planSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var out domain.PlanStepOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
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
func (e *Executor) runGenerateSQL(ctx context.Context, sc stepContext, run domain.Run, plan domain.PlanStepOutput, log *slog.Logger) (string, error) {
	payload, err := e.buildPayload(ctx, sc.workspaceID, run.QuestionText, plan.Approach)
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, generateSQLSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var out domain.GenerateSQLStepOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
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
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
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

// runNarrate executes the narrate step (FEAT-015): the model writes plain-language prose explaining
// what the facts show. It narrates ONLY over the execute result set carried by the facts payload —
// never over data outside the result set, and never over withheld column values (BuildFactsPayload
// drops them before the payload is sealed).
func (e *Executor) runNarrate(ctx context.Context, sc stepContext, run domain.Run, out domain.ExecuteStepOutput, log *slog.Logger) (string, error) {
	payload, err := e.buildFactsPayload(ctx, sc.workspaceID, run.QuestionText, "", out)
	if err != nil {
		return "", err
	}

	raw, err := e.client.Complete(ctx, sc.workspaceID, sc.userID, narrateSystemPrompt, payload)
	if err != nil {
		return "", err
	}

	var outNarrate domain.NarrateStepOutput
	if err := json.Unmarshal([]byte(raw), &outNarrate); err != nil {
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
// chart_spec step's spec, an empty provenance_links array (FEAT-030 is Phase 3), and the measured
// reconciliation summary. A run whose steps all succeeded but whose Report cannot be persisted is a
// corrupt state, so this fails the run rather than being best-effort.
func (e *Executor) createReport(ctx context.Context, sc stepContext, run domain.Run, reconcile domain.ReconciliationSummary, chart domain.ChartSpec, narrative string) error {
	report := domain.Report{
		ID:                    uuid.NewString(),
		WorkspaceID:           sc.workspaceID,
		RunID:                 sc.runID,
		Title:                 deriveReportTitle(run.QuestionText),
		Narrative:             narrative,
		ChartSpec:             chart,
		ProvenanceLinks:       []string{},
		ReconciliationSummary: reconcile,
		ParentReportID:        "",
		CreatedAt:             time.Now().UTC(),
	}
	if err := e.reports.Create(ctx, report); err != nil {
		return fmt.Errorf("persisting report: %w", err)
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

	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		path, cleanup, err := e.downloadToTemp(ctx, storage.NormalizedDatasetKey(workspaceID, ds.ID), ".parquet")
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
func (e *Executor) downloadToTemp(ctx context.Context, key, ext string) (string, func(), error) {
	reader, err := e.objects.GetObject(ctx, key)
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
// and semantic layer, and never guess. The model's reply must be JSON matching PlanStepOutput.
const planSystemPrompt = `You are the planning step of a grounded analytics system. You will be given a user's question and the schema/profile metadata of the datasets available in their workspace, plus any confirmed relationships, metric definitions, and corrections that form the workspace's semantic layer.

Your job is to decide, from the schema and semantic layer alone, whether the question can be answered against this workspace's data — NOT to answer it yourself, and NEVER to invent data or make up values. There is no code path where you answer from data held in the prompt: only the schema and metadata are here.

Respond with ONLY a JSON object of the form {"answerable": true|false, "reason": "...", "approach": "..."}.
- Set answerable to true only when the question is clear and the workspace's tables/relationships/metrics suffice to answer it.
- When answerable is false, set reason to a short, plain-language explanation of what is missing or ambiguous (e.g. "The data has no column that records region." or "It is unclear whether you mean revenue by count or by total value."), and omit approach.
- When answerable is true, set approach to a concise plan for the SQL step: which tables, joins, and metrics it should use.
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

// narrateSystemPrompt frames the narrate step (FEAT-015): the model receives ONLY the executed
// result set (the facts) plus the workspace's schema metadata — it narrates over the facts and
// nothing else. Every stated number must be traceable to the result set (FEAT-030's eligibility),
// and the prose must stand alone for someone who never sees the SQL or schema.
const narrateSystemPrompt = `You are the narrative step of a grounded analytics system. You are given the executed result set of a user's question: the actual data rows the query returned, alongside the workspace's schema metadata.

Your job is to write a clear, plain-language answer to the user's question that explains what the data shows. The result set is the ONLY data you may reference — never invent, estimate, or reach for numbers that are not present in the rows you were given.

Rules:
- Write prose a non-technical reader can act on. No SQL, no column names as jargon, no schema terminology.
- Every number you state must come from the provided result set, and each one should be stated so a reader could trace it back to a specific value (it will be eligible for provenance linking).
- If the result set contains values that a human should treat carefully (for example a null in a key cell or an obvious outlier), mention it plainly rather than glossing over it.
- Keep the narrative focused and proportionate to the question. Do not pad.
- Do not restate the raw table row by row; summarize, compare, and call out what matters.

Respond with ONLY a JSON object of the form {"narrative": "..."} containing the narrative as a single string. Escape any embedded double quotes. Do not include markdown fences or any text outside the JSON object.`

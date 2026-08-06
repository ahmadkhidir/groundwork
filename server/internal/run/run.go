// Package run implements FEAT-013's linear persisted executor: the component that takes a created
// run and advances it — engine wake, plan, generate_sql, execute — writing every boundary to real
// runs/steps rows as it goes.
//
// This is the Phase 2 boundary in concrete form (IMPLEMENTATION.md "Phase 2 boundary assumptions"):
// a LINEAR executor with no leases, heartbeats, forks, or resume. A step is created the moment its
// work begins and completed or failed in a single follow-up write; the per-step idempotency key
// (runID:stepName) is persisted so the schema's retry-safety guarantee is present from day one, but
// no retry machinery runs this wave. static_check, reconcile, shadow_check, adversarial_review,
// chart_spec, narrate, and assemble are Phase 3/5 and deliberately not implemented here.
//
// This package is the ONLY route to the model that exists at runtime: the executor builds every
// payload through model.BuildPayload (the single shared construction function risk_log.md R-016
// requires — see model/payload.go's package doc for why the type system enforces it) and transmits
// it through model.Client, which writes the egress log in the same code path as the outbound call
// (behavior rule 7).
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
	_, err = e.execStep(ctx, sc, domain.StepExecute, generateSQLOutput, log, func() (string, error) {
		return e.runExecute(ctx, sc, handle, genSQL.SQL, log)
	})
	if err != nil {
		log.Error("execute step failed", slog.String("error", err.Error()))
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

// runExecute executes the generated SQL against the workspace's engine and returns the typed
// ExecuteStepOutput — the facts FEAT-014 returns, converted to JSON scalars at this boundary.
func (e *Executor) runExecute(ctx context.Context, sc stepContext, handle engine.Handle, sql string, log *slog.Logger) (string, error) {
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

// buildPayload assembles the single shared model payload for a model-calling step: the grounding
// context from the workspace's semantic layer, plus every complete dataset's column profile under
// its engine view name. This is the FEAT-013 / R-016 seam — every payload for every step comes from
// model.BuildPayload, never from per-step ad hoc assembly.
func (e *Executor) buildPayload(ctx context.Context, workspaceID, question, priorContext string) (model.Payload, error) {
	grounding, err := semantic.AssembleGroundingContext(ctx, workspaceID, e.relationships, e.metrics, e.corrections)
	if err != nil {
		return model.Payload{}, err
	}

	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return model.Payload{}, fmt.Errorf("listing datasets: %w", err)
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
			return model.Payload{}, fmt.Errorf("listing columns for %s: %w", ds.ID, err)
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
		return model.Payload{}, errors.New("workspace has no complete datasets to query")
	}

	return model.BuildPayload(question, priorContext, tables, grounding), nil
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

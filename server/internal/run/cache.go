package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/events"
	"github.com/ahmadkhidir/groundwork/server/internal/model"
	"github.com/ahmadkhidir/groundwork/server/internal/orchestrator"
)

// tryCacheHit is the FEAT-027 determinism-cache fast path: if a deterministically identical run
// (same question, same schema, same seed SQL) already completed, replay its persisted outputs and
// finish WITHOUT waking the engine or asking the model.
//
// It returns true only when the cached run was fully replayed and marked complete; on a miss
// (orchestrator.ErrCacheMiss), a hash/store failure, or ANY error mid-replay it logs and returns
// false so the caller falls through to normal execution. Caching is a best-effort accelerator, never
// a correctness gate — the cache can fail and the run still executes correctly.
func (e *Executor) tryCacheHit(ctx context.Context, sc stepContext, userID string, run domain.Run, seedSQL *string, log *slog.Logger) bool {
	qHash, schemaHash, err := e.cacheDigest(ctx, sc.workspaceID, run.QuestionText)
	if err != nil {
		log.Warn("cache: hashing run", slog.String("error", err.Error()))
		return false
	}
	seed := ""
	if seedSQL != nil {
		seed = *seedSQL
	}

	record, err := e.cache.Get(ctx, sc.workspaceID, qHash, schemaHash, seed)
	if errors.Is(err, orchestrator.ErrCacheMiss) {
		return false
	}
	if err != nil {
		log.Warn("cache: lookup failed", slog.String("error", err.Error()))
		return false
	}

	log.Info("cache: hit, replaying outputs", slog.String("source_run_id", record.SourceRunID))
	if err := e.replayCacheHit(ctx, sc, run, record, log); err != nil {
		log.Warn("cache: replay failed, falling through to normal execution", slog.String("error", err.Error()))
		return false
	}
	if err := e.setRunStatus(ctx, log, run.ID, domain.RunComplete); err != nil {
		log.Error("cache: marking run complete", slog.String("error", err.Error()))
		return false
	}
	log.Info("run complete (cached)")
	return true
}

// rememberCache persists this successful run's outputs under a determinism-identical key (FEAT-027),
// so a later identical run replays them. It is called only AFTER the report is materialized; a
// failure to persist is best-effort by design (logged by the caller, does not fail the run).
//
// Pass-1 boundary (recorded in the wave report): the cache key gates on schema SHAPE (columns, types,
// metrics, relationships), not the underlying data rows — so a replaced Parquet with identical shape
// still matches. SeedSQL is part of the key, so an edited-SQL run never shadows the original
// question's answer.
func (e *Executor) rememberCache(ctx context.Context, sc stepContext, run domain.Run, seedSQL *string, planOutput, generateSQLOutput, executeOutput, chartSpecOutput, narrateOutput string, reconcile domain.ReconciliationSummary) error {
	qHash, schemaHash, err := e.cacheDigest(ctx, sc.workspaceID, run.QuestionText)
	if err != nil {
		return err
	}
	reconcileJSON, err := json.Marshal(reconcile)
	if err != nil {
		return fmt.Errorf("encoding reconciliation summary: %w", err)
	}
	seed := ""
	if seedSQL != nil {
		seed = *seedSQL
	}
	record := orchestrator.CacheRecord{
		WorkspaceID:        sc.workspaceID,
		QuestionHash:       qHash,
		SchemaHash:         schemaHash,
		SeedSQL:            seed,
		SourceRunID:        sc.runID,
		PlanOutput:         planOutput,
		GenerateSQLOutput:  generateSQLOutput,
		ExecuteOutput:      executeOutput,
		ChartSpecOutput:    chartSpecOutput,
		NarrateOutput:      narrateOutput,
		ReconciliationJSON: string(reconcileJSON),
	}
	return e.cache.Put(ctx, record)
}

// cacheDigest computes the two halves of the FEAT-027 cache key for a run: sha256 of the
// whitespace-normalized question, and sha256 of a canonical serialization of the workspace's
// queryable schema (the same table/column surface the model is shown, in stable ListByWorkspace
// order).
func (e *Executor) cacheDigest(ctx context.Context, workspaceID, question string) (qHash, schemaHash string, err error) {
	qHash = sha256Hex(strings.Join(strings.Fields(strings.TrimSpace(question)), " "))
	schemaHash, err = e.schemaDigest(ctx, workspaceID)
	if err != nil {
		return "", "", fmt.Errorf("hashing schema: %w", err)
	}
	return qHash, schemaHash, nil
}

// schemaDigest returns sha256 over the JSON of the workspace's queryable schema surface, folded
// together with a per-dataset DATA-VERSION stamp (FEAT-027 AC2: "invalidated automatically when the
// underlying schema or data changes").
//
// The stamp carries each complete dataset's id, row_count, and created_at. It is deliberately
// combined even though the schema surface already embeds DatasetID, so that a data change is
// invalidated by the stamp's own terms and not only by the incidental fact that a replacement
// usually lands as a new row id: if a future path ever updated a completed dataset's contents in
// place, the row_count/created_at half of the stamp still changes the key. Data is otherwise
// immutable per fully-ingested dataset (every re-upload, Replace, or Keep both creates a new
// Dataset row), so this metadata-only read — no data scan, no engine query — is the cheap O(1)
// data-version signal for the whole cache.
func (e *Executor) schemaDigest(ctx context.Context, workspaceID string) (string, error) {
	tables, err := e.assembleTables(ctx, workspaceID)
	if err != nil {
		return "", err
	}

	datasets, err := e.datasets.ListByWorkspace(ctx, workspaceID)
	if err != nil {
		return "", fmt.Errorf("listing datasets for data version: %w", err)
	}
	type dataVersion struct {
		ID        string `json:"id"`
		RowCount  int64  `json:"row_count"`
		CreatedAt string `json:"created_at"`
	}
	var stamps []dataVersion
	for _, ds := range datasets {
		if ds.IngestStatus != domain.IngestComplete {
			continue
		}
		stamps = append(stamps, dataVersion{
			ID:        ds.ID,
			RowCount:  ds.RowCount,
			CreatedAt: ds.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}

	b, err := json.Marshal(struct {
		Tables []model.TableMetadata `json:"schema"`
		Data   []dataVersion         `json:"data_version"`
	}{Tables: tables, Data: stamps})
	if err != nil {
		return "", err
	}
	return sha256Hex(string(b)), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// replayCacheHit reconstructs a cached run inside the durable step graph: it creates+completes each
// step row carrying the cached output (no model/engine work), publishes the cache.hit live event so
// the viewer surfaces the "cached from run" reference, and materializes the Report from the cached
// outputs + measured reconciliation — the same composition a freshly executed run gets.
func (e *Executor) replayCacheHit(ctx context.Context, sc stepContext, run domain.Run, rec orchestrator.CacheRecord, log *slog.Logger) error {
	if err := e.setRunStatus(ctx, log, run.ID, domain.RunRunning); err != nil {
		return err
	}
	e.publish(run.ID, events.Event{
		Type:        events.TypeCacheHit,
		RunID:       run.ID,
		Status:      string(domain.RunRunning),
		CachedRunID: rec.SourceRunID,
	})

	var plan domain.PlanStepOutput
	if rec.PlanOutput != "" {
		planOutput, err := e.replayStep(ctx, sc, log, domain.StepPlan, "", rec.PlanOutput)
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(planOutput), &plan); err != nil {
			return fmt.Errorf("parsing cached plan output: %w", err)
		}
	}

	genSQLOut, err := e.replayStep(ctx, sc, log, domain.StepGenerateSQL, rec.PlanOutput, rec.GenerateSQLOutput)
	if err != nil {
		return err
	}
	execOut, err := e.replayStep(ctx, sc, log, domain.StepExecute, genSQLOut, rec.ExecuteOutput)
	if err != nil {
		return err
	}
	chartOut, err := e.replayStep(ctx, sc, log, domain.StepChartSpec, execOut, rec.ChartSpecOutput)
	if err != nil {
		return err
	}
	narrateJSON, err := e.replayStep(ctx, sc, log, domain.StepNarrate, execOut, rec.NarrateOutput)
	if err != nil {
		return err
	}

	var chartSpecOut domain.ChartSpecStepOutput
	if err := json.Unmarshal([]byte(chartOut), &chartSpecOut); err != nil {
		return fmt.Errorf("parsing cached chart_spec output: %w", err)
	}
	var narrateOut domain.NarrateStepOutput
	if err := json.Unmarshal([]byte(narrateJSON), &narrateOut); err != nil {
		return fmt.Errorf("parsing cached narrate output: %w", err)
	}
	var reconcile domain.ReconciliationSummary
	if err := json.Unmarshal([]byte(rec.ReconciliationJSON), &reconcile); err != nil {
		return fmt.Errorf("parsing cached reconciliation summary: %w", err)
	}

	return e.createReport(ctx, sc, run, reconcile, chartSpecOut.ChartSpec, narrateOut.Narrative)
}

// replayStep creates and immediately completes a step row carrying a cached output (FEAT-027
// replay). Unlike execStep it performs no work: the output already exists, so it is persisted and
// its completion event is emitted directly.
func (e *Executor) replayStep(ctx context.Context, sc stepContext, log *slog.Logger, name domain.StepName, input, output string) (string, error) {
	now := time.Now().UTC()
	step := domain.Step{
		ID:             uuid.NewString(),
		RunID:          sc.runID,
		StepName:       name,
		Status:         domain.StepComplete,
		Input:          input,
		Output:         output,
		IdempotencyKey: fmt.Sprintf("%s:%s", sc.runID, name),
		StartedAt:      &now,
	}
	if err := e.steps.Create(ctx, sc.workspaceID, step); err != nil {
		return "", err
	}
	e.publish(sc.runID, events.Event{
		Type:       events.TypeStepCompleted,
		RunID:      sc.runID,
		StepName:   string(name),
		StepStatus: string(domain.StepComplete),
		Output:     output,
	})
	return output, nil
}

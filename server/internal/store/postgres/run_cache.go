package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/orchestrator"
)

// RunCacheRepository persists and reads FEAT-027 determinism-cache records.
//
// It satisfies orchestrator.CacheStore so internal/run's executor can be wired with it. Put upserts
// on the record's key — a deterministically-identical later run overwrites the prior entry rather
// than duplicating it — and Get returns orchestrator.ErrCacheMiss when no record matches, so the
// executor treats a miss as "run it normally" and a real storage error as "log and run normally
// anyway" (caching is a best-effort accelerator, never a correctness gate).
type RunCacheRepository struct {
	pool *Pool
}

// NewRunCacheRepository constructs a RunCacheRepository backed by pool.
func NewRunCacheRepository(pool *Pool) *RunCacheRepository {
	return &RunCacheRepository{pool: pool}
}

// Get returns the cached record for the exact key, or orchestrator.ErrCacheMiss.
func (r *RunCacheRepository) Get(ctx context.Context, workspaceID, questionHash, schemaHash, seedSQL string) (orchestrator.CacheRecord, error) {
	var (
		rec      orchestrator.CacheRecord
		reconcil string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT workspace_id, question_hash, schema_hash, seed_sql, source_run_id,
		        plan_output, generate_sql_output, execute_output, chart_spec_output, narrate_output,
		        reconciliation_json
		 FROM run_cache
		 WHERE workspace_id = $1 AND question_hash = $2 AND schema_hash = $3 AND seed_sql = $4`,
		workspaceID, questionHash, schemaHash, seedSQL,
	).Scan(
		&rec.WorkspaceID, &rec.QuestionHash, &rec.SchemaHash, &rec.SeedSQL, &rec.SourceRunID,
		&rec.PlanOutput, &rec.GenerateSQLOutput, &rec.ExecuteOutput, &rec.ChartSpecOutput, &rec.NarrateOutput,
		&reconcil,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return orchestrator.CacheRecord{}, orchestrator.ErrCacheMiss
		}
		return orchestrator.CacheRecord{}, fmt.Errorf("getting run cache: %w", err)
	}
	rec.ReconciliationJSON = reconcil
	return rec, nil
}

// Put stores record under its key, upserting on collision.
func (r *RunCacheRepository) Put(ctx context.Context, rec orchestrator.CacheRecord) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO run_cache
		   (workspace_id, question_hash, schema_hash, seed_sql, source_run_id,
		    plan_output, generate_sql_output, execute_output, chart_spec_output, narrate_output,
		    reconciliation_json, cached_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::jsonb, now())
		 ON CONFLICT (workspace_id, question_hash, schema_hash, seed_sql)
		 DO UPDATE SET source_run_id = EXCLUDED.source_run_id,
		               plan_output = EXCLUDED.plan_output,
		               generate_sql_output = EXCLUDED.generate_sql_output,
		               execute_output = EXCLUDED.execute_output,
		               chart_spec_output = EXCLUDED.chart_spec_output,
		               narrate_output = EXCLUDED.narrate_output,
		               reconciliation_json = EXCLUDED.reconciliation_json,
		               cached_at = now()`,
		rec.WorkspaceID, rec.QuestionHash, rec.SchemaHash, rec.SeedSQL, rec.SourceRunID,
		rec.PlanOutput, rec.GenerateSQLOutput, rec.ExecuteOutput, rec.ChartSpecOutput, rec.NarrateOutput,
		rec.ReconciliationJSON,
	)
	if err != nil {
		return fmt.Errorf("storing run cache: %w", err)
	}
	return nil
}

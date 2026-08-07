package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// UsageRepository persists metered usage records (FEAT-047) — the per-workspace engine-seconds,
// storage bytes, and model tokens collected from day one regardless of billing status, per
// architecture.md's Logging & Monitoring section.
//
// The read side (ListByWorkspace) backs FEAT-047's user-facing Usage view. The write side
// (Upsert, SetStorageBytes) is the metering seam the internal/metering package drives: it satisfies
// metering.UsageUpserter structurally (period boundaries are computed by metering.PeriodFor, so
// this repository stays a period-agnostic SQL layer). Every metering write is BEST-EFFORT at the
// call site — a metering failure must never fail the model call, engine release, or ingest that
// produced the event.
type UsageRepository struct {
	pool *Pool
}

// NewUsageRepository constructs a UsageRepository backed by pool.
func NewUsageRepository(pool *Pool) *UsageRepository {
	return &UsageRepository{pool: pool}
}

// ListByWorkspace returns the workspace's usage records, most recent period first, capped at limit.
// The most recent record is the "current period" for screens/13-workspace-settings.md's stat rows,
// since billing periods are a billing-system concern this wave does not model.
func (r *UsageRepository) ListByWorkspace(ctx context.Context, workspaceID string, limit int) ([]domain.UsageRecord, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, period_start, period_end, engine_seconds, storage_bytes, model_tokens
		 FROM usage_records WHERE workspace_id = $1
		 ORDER BY period_start DESC LIMIT $2`,
		workspaceID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("listing usage records: %w", err)
	}
	defer rows.Close()

	out := []domain.UsageRecord{}
	for rows.Next() {
		var rec domain.UsageRecord
		if err := rows.Scan(&rec.ID, &rec.WorkspaceID, &rec.PeriodStart, &rec.PeriodEnd,
			&rec.EngineSeconds, &rec.StorageBytes, &rec.ModelTokens); err != nil {
			return nil, fmt.Errorf("scanning usage record: %w", err)
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating usage records: %w", err)
	}
	return out, nil
}

// Upsert accumulates engine-seconds and model tokens into the (workspace_id, period_start) record
// identified by the explicit period, creating the record when none exists. It is the ACCUMULATE
// half of the metering write side: engine-seconds add across engine runs and model tokens add
// across model calls, so a second write in the same period adds to the first rather than replacing
// it (metering.UsageWriter's contract).
//
// The conflict path updates ONLY the accumulating columns. storage_bytes is deliberately left
// untouched — it is a point-in-time snapshot owned by SetStorageBytes, and an engine or token
// write must never clobber it. The INSERT column list likewise omits it, so a fresh record starts
// at the column default (0).
func (r *UsageRepository) Upsert(ctx context.Context, workspaceID string, periodStart, periodEnd time.Time, engineSeconds float64, modelTokens int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO usage_records (id, workspace_id, period_start, period_end, engine_seconds, model_tokens)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (workspace_id, period_start) DO UPDATE
		 SET engine_seconds = usage_records.engine_seconds + EXCLUDED.engine_seconds,
		     model_tokens   = usage_records.model_tokens + EXCLUDED.model_tokens`,
		uuid.NewString(), workspaceID, periodStart, periodEnd, engineSeconds, modelTokens,
	)
	if err != nil {
		return fmt.Errorf("upserting usage record: %w", err)
	}
	return nil
}

// SetStorageBytes replaces the (workspace_id, period_start) record's storage_bytes snapshot with
// bytes, creating the record when none exists. It is the REPLACE half of the metering write side:
// storage_bytes is a point-in-time reading ("as of period_end" per schema.md), so a reconciliation
// or post-delete recompute writes the current total, never a delta — a second write in the same
// period supersedes the first.
//
// The conflict path updates ONLY storage_bytes. engine_seconds and model_tokens are deliberately
// left untouched — they are accumulated by Upsert, and a storage snapshot must never clobber them.
// The INSERT column list likewise omits them, so a fresh record starts at the column defaults (0).
func (r *UsageRepository) SetStorageBytes(ctx context.Context, workspaceID string, periodStart, periodEnd time.Time, bytes int64) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO usage_records (id, workspace_id, period_start, period_end, storage_bytes)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (workspace_id, period_start) DO UPDATE
		 SET storage_bytes = EXCLUDED.storage_bytes`,
		uuid.NewString(), workspaceID, periodStart, periodEnd, bytes,
	)
	if err != nil {
		return fmt.Errorf("setting usage storage bytes: %w", err)
	}
	return nil
}

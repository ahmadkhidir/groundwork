package postgres

import (
	"context"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// UsageRepository reads metered usage records (FEAT-047) — the per-workspace engine-seconds,
// storage bytes, and model tokens collected from day one regardless of billing status, per
// architecture.md's Logging & Monitoring section.
//
// This wave is read-only: the write side of the meter (recording engine-seconds as engines acquire,
// storage as datasets land, tokens as model calls happen) is owned by the metering work the docs
// schedule independently of this feature; FEAT-047's scope is the user-facing view over whatever
// records exist, with an honest empty state when none do.
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

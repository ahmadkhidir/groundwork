package postgres

import (
	"context"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// CorrectionRepository persists and retrieves durable user corrections — schema.md's Correction
// entity, per FEAT-019 (storage) and FEAT-020 (capture, a later wave). This wave (FEAT-019) owns
// storage and the read side only: screens/15-semantic-layer.md's Corrections tab, and the log a
// correction is later attributed to.
//
// Create exists so the storage contract is complete and testable even though nothing calls it yet —
// FEAT-020 (Persistent User Corrections, this same Phase 2 roadmap phase but a later wave) is what
// actually captures a correction while a user edits SQL or a semantic-layer definition mid-run. Until
// that wave lands, ListByWorkspace legitimately returns an empty slice for every workspace, and
// screens/15's Corrections tab shows its documented Empty state rather than fabricated rows.
type CorrectionRepository struct {
	pool *Pool
}

// NewCorrectionRepository constructs a CorrectionRepository backed by pool.
func NewCorrectionRepository(pool *Pool) *CorrectionRepository {
	return &CorrectionRepository{pool: pool}
}

// Create inserts a new correction row.
func (r *CorrectionRepository) Create(ctx context.Context, c domain.Correction) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO corrections (id, workspace_id, description, original_logic, corrected_logic, originating_run_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		c.ID, c.WorkspaceID, c.Description, c.OriginalLogic, c.CorrectedLogic,
		nullableString(c.OriginatingRunID), c.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting correction: %w", err)
	}
	return nil
}

// ListByWorkspace returns every correction in workspaceID, most recent first —
// screens/15-semantic-layer.md's Corrections tab describes this as "a chronological log", and the
// FEAT-019 grounding-context assembler's source of truth for accumulated corrections.
//
// A correction whose originating run has since been deleted (migration 00005's ON DELETE SET NULL)
// comes back with OriginatingRunID == "" — screens/15's documented edge case ("the correction itself
// remains valid and listed, but the 'view originating run' link is disabled"), which the API layer
// renders by disabling the link whenever this field is empty rather than by any special-casing here.
func (r *CorrectionRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Correction, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, description, original_logic, corrected_logic, originating_run_id, created_at
		 FROM corrections WHERE workspace_id = $1 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing corrections: %w", err)
	}
	defer rows.Close()

	var out []domain.Correction
	for rows.Next() {
		var (
			c                domain.Correction
			originatingRunID *string
		)
		if err := rows.Scan(&c.ID, &c.WorkspaceID, &c.Description, &c.OriginalLogic, &c.CorrectedLogic,
			&originatingRunID, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning correction: %w", err)
		}
		if originatingRunID != nil {
			c.OriginatingRunID = *originatingRunID
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating corrections: %w", err)
	}
	return out, nil
}

// CountByWorkspace returns the number of corrections in workspaceID.
func (r *CorrectionRepository) CountByWorkspace(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM corrections WHERE workspace_id = $1`, workspaceID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting corrections: %w", err)
	}
	return count, nil
}

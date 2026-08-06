package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrCorrectionNotFound is returned when no correction matches the given id, scoped to a workspace —
// FEAT-020's "user can view and remove a stored correction" (AC3). Returned both when the id never
// existed and when it belongs to a different workspace, so the API can surface an honest 404 for
// either case without ever confirming a cross-workspace row's existence (the same non-enumeration
// stance as ErrRelationshipNotFound / ErrMetricDefinitionNotFound).
var ErrCorrectionNotFound = errors.New("correction not found")

// CorrectionRepository persists and retrieves durable user corrections — schema.md's Correction
// entity, per FEAT-019 (storage) and FEAT-020 (capture + removal). FEAT-019 owned the read side
// (screens/15-semantic-layer.md's Corrections tab); FEAT-020 (the same Phase 2 roadmap phase, a
// later wave) added the write side this file's Create always existed for: the API captures a
// Correction row when a user's edited SQL starts a new run (see api/runs.go), and removes one when
// the user deletes it from the Corrections tab.
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

// Delete removes the correction with id, scoped to workspaceID. Returns ErrCorrectionNotFound if no
// correction matches — including when a row exists but belongs to a different workspace, so a caller
// with a URL pointing at another workspace's correction gets an honest not-found rather than a
// successful no-op delete (compare metric_definitions.DeleteForWorkspace, whose handler checks
// GetByIDForWorkspace first; the corrections handler relies on this scope-scoped return instead).
//
// Unlike DeleteForWorkspace's no-op posture, scoping a delete and reporting not-found here is
// deliberate: the rows-affected count is the only in-hand proof of ownership, and FEAT-020 AC3's
// remove action must not silently succeed for a row it did not touch.
func (r *CorrectionRepository) Delete(ctx context.Context, workspaceID, id string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM corrections WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("deleting correction: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrCorrectionNotFound
	}
	return nil
}

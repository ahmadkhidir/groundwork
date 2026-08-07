package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrDeletionRecordNotFound is returned when no deletion proof exists for the given lookup — the
// workspace either was never deleted, or its deletion predates proof recording.
var ErrDeletionRecordNotFound = errors.New("deletion record not found")

// DeletionRecordRepository reads workspace-deletion proof records from the deletion_records table
// (migration 00001) — the deliberate exception to that migration's cascade rule, because a proof must
// SURVIVE the workspace it describes.
//
// Writing is deliberately NOT a method here. A proof is written atomically with the workspace row's
// deletion by WorkspaceRepository.DeleteWithProof, so no code path can leave a proof row claiming
// completion while the workspace still exists — a false deletion record would be exactly the
// dishonesty FEAT-043's "provable deletion" exists to rule out. This repository is the read side of
// that contract, serving the Deletion Proof screen's follow-up GET (screens/14).
type DeletionRecordRepository struct {
	pool *Pool
}

// NewDeletionRecordRepository constructs a DeletionRecordRepository backed by pool.
func NewDeletionRecordRepository(pool *Pool) *DeletionRecordRepository {
	return &DeletionRecordRepository{pool: pool}
}

// GetByWorkspace returns the deletion proof for workspaceID, or ErrDeletionRecordNotFound.
//
// The workspace itself is gone by definition — the record is the proof — so there is no workspace
// row to check ownership against. Handlers authorize this read against the record's initiated_by
// instead (only the user who deleted the workspace may read its proof).
func (r *DeletionRecordRepository) GetByWorkspace(ctx context.Context, workspaceID string) (domain.DeletionRecord, error) {
	var (
		rec  domain.DeletionRecord
		cats []string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, initiated_by, deleted_categories, completed_at
		 FROM deletion_records WHERE workspace_id = $1`,
		workspaceID,
	).Scan(&rec.ID, &rec.WorkspaceID, &rec.InitiatedBy, &cats, &rec.CompletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.DeletionRecord{}, ErrDeletionRecordNotFound
		}
		return domain.DeletionRecord{}, fmt.Errorf("getting deletion record: %w", err)
	}
	rec.DeletedCategories = make([]domain.DeletedCategory, len(cats))
	for i, c := range cats {
		rec.DeletedCategories[i] = domain.DeletedCategory(c)
	}
	return rec, nil
}

package postgres

import (
	"context"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// RelationshipRepository persists confirmed relationships — the semantic layer row created when a
// CandidateJoin is accepted (FEAT-009's write side of FEAT-008), per schema.md's Relationship entity.
//
// FEAT-019 (Semantic Layer Storage, the full read/write surface — listing, editing, and using
// relationships as query-planning grounding) is Phase 2 scope and NOT authorized this wave. This
// repository is scoped narrowly to what FEAT-009 itself needs: writing the row so an accepted join is
// durably recorded rather than left unpersisted, without building any of Phase 2's semantic-layer
// features around it.
type RelationshipRepository struct {
	pool *Pool
}

// NewRelationshipRepository constructs a RelationshipRepository backed by pool.
func NewRelationshipRepository(pool *Pool) *RelationshipRepository {
	return &RelationshipRepository{pool: pool}
}

// Create inserts a new confirmed relationship row.
func (r *RelationshipRepository) Create(ctx context.Context, rel domain.Relationship) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO relationships
		   (id, workspace_id, source_column_id, target_column_id, cardinality, grain_notes, confirmed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		rel.ID, rel.WorkspaceID, rel.SourceColumnID, rel.TargetColumnID,
		string(rel.Cardinality), rel.GrainNotes, rel.ConfirmedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting relationship: %w", err)
	}
	return nil
}

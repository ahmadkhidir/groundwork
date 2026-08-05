package postgres

import (
	"context"
	"fmt"
)

// SemanticSummaryRepository computes the cheap aggregate counts screens/04-workspace-home.md's
// semantic layer summary panel needs ("12 relationships, 4 metric definitions — view all").
//
// Kept as its own tiny repository rather than reusing RelationshipRepository.CountByWorkspace /
// MetricDefinitionRepository.CountByWorkspace individually: the summary endpoint always wants both
// counts together in one round trip pair, and giving it a single collaborator keeps
// RegisterSemanticRoutes' summary handler from taking on two separate repository dependencies for
// what is conceptually one read.
type SemanticSummaryRepository struct {
	pool *Pool
}

// NewSemanticSummaryRepository constructs a SemanticSummaryRepository backed by pool.
func NewSemanticSummaryRepository(pool *Pool) *SemanticSummaryRepository {
	return &SemanticSummaryRepository{pool: pool}
}

// CountRelationships returns the number of confirmed relationships in workspaceID.
func (r *SemanticSummaryRepository) CountRelationships(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM relationships WHERE workspace_id = $1`, workspaceID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting relationships: %w", err)
	}
	return count, nil
}

// CountMetricDefinitions returns the number of metric definitions in workspaceID.
func (r *SemanticSummaryRepository) CountMetricDefinitions(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM metric_definitions WHERE workspace_id = $1`, workspaceID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting metric definitions: %w", err)
	}
	return count, nil
}

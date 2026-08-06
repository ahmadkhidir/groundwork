package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrRelationshipNotFound is returned when no relationship matches the given id (optionally scoped
// to a workspace).
var ErrRelationshipNotFound = errors.New("relationship not found")

// RelationshipRepository persists and retrieves confirmed relationships — the semantic layer rows
// created when a CandidateJoin is accepted (FEAT-009's write side of FEAT-008) or, per FEAT-019 (this
// wave), reviewed and edited on screens/15-semantic-layer.md's Relationships tab.
//
// Create shipped with FEAT-009 in Phase 1, deliberately Create-only at the time — FEAT-019 (Phase 2)
// had not been authorized yet. ListByWorkspace, GetByIDForWorkspace, and Update land with FEAT-019
// itself: this is the same relationships table, so the full read/write surface extends this
// repository rather than standing up a second reader over the same rows.
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

// ListByWorkspace returns every confirmed relationship in workspaceID, most recently confirmed
// first — screens/15-semantic-layer.md's Relationships tab, and the FEAT-019 grounding-context
// assembler's source of truth for confirmed joins.
func (r *RelationshipRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Relationship, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, source_column_id, target_column_id, cardinality, grain_notes, confirmed_at
		 FROM relationships WHERE workspace_id = $1 ORDER BY confirmed_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing relationships: %w", err)
	}
	defer rows.Close()

	var out []domain.Relationship
	for rows.Next() {
		rel, err := scanRelationship(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating relationships: %w", err)
	}
	return out, nil
}

// GetByIDForWorkspace returns the relationship with id, scoped to workspaceID, or
// ErrRelationshipNotFound if it doesn't exist or belongs to a different workspace — the same
// non-enumeration pattern GetByIDForWorkspace uses elsewhere in this package (see columns.go).
func (r *RelationshipRepository) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Relationship, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, source_column_id, target_column_id, cardinality, grain_notes, confirmed_at
		 FROM relationships WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	rel, err := scanRelationshipRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Relationship{}, ErrRelationshipNotFound
		}
		return domain.Relationship{}, fmt.Errorf("getting relationship for workspace: %w", err)
	}
	return rel, nil
}

// DatasetRelationship is a confirmed relationship annotated with its source/target column names, for
// FEAT-048's delete-confirmation disclosure — naming what a dataset deletion would take with it is
// the explicit acceptance criterion, not merely a count.
type DatasetRelationship struct {
	domain.Relationship
	SourceColumnName string
	TargetColumnName string
}

// ListByDatasetColumns returns every confirmed relationship in workspaceID whose source OR target
// column belongs to datasetID, annotated with both columns' names.
//
// FEAT-048's delete confirmation must name any dependent Relationship before the user commits, rather
// than silently orphaning it once the CASCADE (migration 00001) removes the columns it points to —
// this is the read that powers that disclosure. Ordered by confirmed_at DESC to match
// ListByWorkspace's convention.
func (r *RelationshipRepository) ListByDatasetColumns(ctx context.Context, workspaceID, datasetID string) ([]DatasetRelationship, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT rel.id, rel.workspace_id, rel.source_column_id, rel.target_column_id,
		       rel.cardinality, rel.grain_notes, rel.confirmed_at, sc.name, tc.name
		FROM relationships rel
		JOIN columns sc ON sc.id = rel.source_column_id
		JOIN columns tc ON tc.id = rel.target_column_id
		WHERE rel.workspace_id = $1 AND (sc.dataset_id = $2 OR tc.dataset_id = $2)
		ORDER BY rel.confirmed_at DESC`,
		workspaceID, datasetID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing dataset relationships: %w", err)
	}
	defer rows.Close()

	var out []DatasetRelationship
	for rows.Next() {
		var (
			dr          DatasetRelationship
			cardinality string
		)
		if err := rows.Scan(&dr.ID, &dr.WorkspaceID, &dr.SourceColumnID, &dr.TargetColumnID,
			&cardinality, &dr.GrainNotes, &dr.ConfirmedAt, &dr.SourceColumnName, &dr.TargetColumnName); err != nil {
			return nil, fmt.Errorf("scanning dataset relationship: %w", err)
		}
		dr.Cardinality = domain.Cardinality(cardinality)
		out = append(out, dr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating dataset relationships: %w", err)
	}
	return out, nil
}

// Update overrides a confirmed relationship's cardinality and grain notes per FEAT-019's Edit action
// on screens/15-semantic-layer.md's Relationships tab. Per that screen's edge case, editing a
// relationship does not retroactively alter an in-progress run's already-generated SQL — it only
// changes what the NEXT run's grounding context sees, which falls out naturally here: this repository
// has no notion of an in-flight run at all.
func (r *RelationshipRepository) Update(ctx context.Context, id string, cardinality domain.Cardinality, grainNotes string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE relationships SET cardinality = $2, grain_notes = $3 WHERE id = $1`,
		id, string(cardinality), grainNotes,
	)
	if err != nil {
		return fmt.Errorf("updating relationship: %w", err)
	}
	return nil
}

// scanRow is the subset of pgx.Rows/pgx.Row that scanRelationshipRow needs, so ListByWorkspace (which
// scans pgx.Rows across many results) and GetByIDForWorkspace (which scans a single pgx.Row) can
// share one column list and one struct-population routine.
type scanRow interface {
	Scan(dest ...any) error
}

func scanRelationshipRow(row scanRow) (domain.Relationship, error) {
	var (
		rel         domain.Relationship
		cardinality string
	)
	if err := row.Scan(&rel.ID, &rel.WorkspaceID, &rel.SourceColumnID, &rel.TargetColumnID,
		&cardinality, &rel.GrainNotes, &rel.ConfirmedAt); err != nil {
		return domain.Relationship{}, err
	}
	rel.Cardinality = domain.Cardinality(cardinality)
	return rel, nil
}

func scanRelationship(rows pgx.Rows) (domain.Relationship, error) {
	rel, err := scanRelationshipRow(rows)
	if err != nil {
		return domain.Relationship{}, fmt.Errorf("scanning relationship: %w", err)
	}
	return rel, nil
}

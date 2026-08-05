package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrMetricDefinitionNotFound is returned when no metric definition matches the given id (scoped to
// a workspace).
var ErrMetricDefinitionNotFound = errors.New("metric definition not found")

// ErrMetricDefinitionNameTaken is returned when a metric definition's name would collide with an
// existing one in the same workspace — migration 00001's UNIQUE (workspace_id, name) constraint,
// surfaced as a typed error rather than a raw pgx unique-violation for callers to check.
var ErrMetricDefinitionNameTaken = errors.New("a metric with this name already exists in this workspace")

// MetricDefinitionRepository persists and retrieves named business metric definitions — schema.md's
// MetricDefinition entity, per FEAT-019 (Semantic Layer Storage). Full CRUD: screens/15-semantic-layer.md
// specifies an inline/modal editor with Save/Cancel and PATCH-updates-in-place for existing
// definitions, and FEAT-019's effort/scope covers the full semantic-layer store, not read-only
// display of rows FEAT-009 alone happens to produce (unlike Relationship, nothing upstream of this
// feature ever creates a MetricDefinition).
type MetricDefinitionRepository struct {
	pool *Pool
}

// NewMetricDefinitionRepository constructs a MetricDefinitionRepository backed by pool.
func NewMetricDefinitionRepository(pool *Pool) *MetricDefinitionRepository {
	return &MetricDefinitionRepository{pool: pool}
}

// Create inserts a new metric definition. Returns ErrMetricDefinitionNameTaken if workspaceID
// already has a metric with this name.
func (r *MetricDefinitionRepository) Create(ctx context.Context, m domain.MetricDefinition) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO metric_definitions (id, workspace_id, name, sql_expression, description, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		m.ID, m.WorkspaceID, m.Name, m.SQLExpression, m.Description, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrMetricDefinitionNameTaken
		}
		return fmt.Errorf("inserting metric definition: %w", err)
	}
	return nil
}

// ListByWorkspace returns every metric definition in workspaceID, alphabetically by name —
// screens/15-semantic-layer.md's Metric Definitions tab and the FEAT-019 grounding-context
// assembler's source of truth.
func (r *MetricDefinitionRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.MetricDefinition, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, name, sql_expression, description, created_at, updated_at
		 FROM metric_definitions WHERE workspace_id = $1 ORDER BY name ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing metric definitions: %w", err)
	}
	defer rows.Close()

	var out []domain.MetricDefinition
	for rows.Next() {
		m, err := scanMetricDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning metric definition: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating metric definitions: %w", err)
	}
	return out, nil
}

// GetByIDForWorkspace returns the metric definition with id, scoped to workspaceID, or
// ErrMetricDefinitionNotFound if it doesn't exist or belongs to a different workspace.
func (r *MetricDefinitionRepository) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.MetricDefinition, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, name, sql_expression, description, created_at, updated_at
		 FROM metric_definitions WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	m, err := scanMetricDefinition(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.MetricDefinition{}, ErrMetricDefinitionNotFound
		}
		return domain.MetricDefinition{}, fmt.Errorf("getting metric definition for workspace: %w", err)
	}
	return m, nil
}

// Update overrides a metric definition's name, SQL expression, and description in place, per
// screens/15-semantic-layer.md's PATCH-updates-in-place Edit action. updatedAt is caller-supplied
// (rather than DEFAULT now() at the database) so the handler's response and the persisted row report
// the identical instant, the same pairing discipline columns.go's UpdatePII already uses for
// pii_reviewed_at. Returns ErrMetricDefinitionNameTaken if the new name collides with a different
// metric in the same workspace.
func (r *MetricDefinitionRepository) Update(ctx context.Context, id, name, sqlExpression, description string, updatedAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE metric_definitions SET name = $2, sql_expression = $3, description = $4, updated_at = $5 WHERE id = $1`,
		id, name, sqlExpression, description, updatedAt,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrMetricDefinitionNameTaken
		}
		return fmt.Errorf("updating metric definition: %w", err)
	}
	return nil
}

// DeleteForWorkspace removes the metric definition with id, scoped to workspaceID. A no-op (not an
// error) if it's already gone or belongs to a different workspace — a delete request racing with
// another delete for the same row should not surface as a server error.
func (r *MetricDefinitionRepository) DeleteForWorkspace(ctx context.Context, id, workspaceID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM metric_definitions WHERE id = $1 AND workspace_id = $2`, id, workspaceID)
	if err != nil {
		return fmt.Errorf("deleting metric definition: %w", err)
	}
	return nil
}

// CountByWorkspace returns the number of metric definitions in workspaceID — one half of
// screens/04-workspace-home.md's semantic layer summary panel ("12 relationships, 4 metric
// definitions").
func (r *MetricDefinitionRepository) CountByWorkspace(ctx context.Context, workspaceID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM metric_definitions WHERE workspace_id = $1`, workspaceID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting metric definitions: %w", err)
	}
	return count, nil
}

func scanMetricDefinition(row scanRow) (domain.MetricDefinition, error) {
	var m domain.MetricDefinition
	if err := row.Scan(&m.ID, &m.WorkspaceID, &m.Name, &m.SQLExpression, &m.Description, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return domain.MetricDefinition{}, err
	}
	return m, nil
}

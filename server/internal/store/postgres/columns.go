package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrColumnNotFound is returned when no column matches the given id.
var ErrColumnNotFound = errors.New("column not found")

// ColumnRepository persists and retrieves profiled columns (FEAT-005), keyed to their owning
// Dataset. Written by the Ingest Pipeline once profiling.Profile computes statistics, and read back
// by the ingest confirmation flow (FEAT-006/007/009/010) and relationship inference (FEAT-008) —
// "profiling statistics are stored and retrievable, not recomputed on every use" is a features.md
// acceptance criterion, not an implementation detail.
type ColumnRepository struct {
	pool *Pool
}

// NewColumnRepository constructs a ColumnRepository backed by pool.
func NewColumnRepository(pool *Pool) *ColumnRepository {
	return &ColumnRepository{pool: pool}
}

// BulkInsert persists every column in cols in a single round trip, scoped to workspaceID.
//
// workspaceID is a separate parameter rather than a domain.Column field: schema.md's Column entity
// (mirrored exactly by domain.Column, which is read-only shared ground truth) has no workspace_id
// property — only dataset_id. The database column exists purely as a denormalization (see migration
// 00001's comment on columns.workspace_id) so a workspace-scoped query never needs to join through
// datasets; it is a storage-layer detail, not part of the domain shape.
func (r *ColumnRepository) BulkInsert(ctx context.Context, workspaceID string, cols []domain.Column) error {
	if len(cols) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for _, c := range cols {
		batch.Queue(
			`INSERT INTO columns
			   (id, dataset_id, workspace_id, name, inferred_type, type_confidence,
			    pii_flag, pii_pattern, excluded_from_model, null_rate, distinct_count)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			c.ID, c.DatasetID, workspaceID, c.Name, string(c.InferredType), c.TypeConfidence,
			c.PIIFlag, c.PIIPattern, c.ExcludedFromModel, c.NullRate, c.DistinctCount,
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range cols {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("inserting column: %w", err)
		}
	}
	return nil
}

// ListByDataset returns every column belonging to datasetID, in stable name order.
func (r *ColumnRepository) ListByDataset(ctx context.Context, datasetID string) ([]domain.Column, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence,
		        pii_flag, pii_pattern, excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE dataset_id = $1 ORDER BY name ASC`,
		datasetID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing columns: %w", err)
	}
	defer rows.Close()
	return scanColumns(rows)
}

// ListByWorkspace returns every column across every dataset in workspaceID — used by relationship
// inference, which compares columns across datasets within the same workspace.
func (r *ColumnRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Column, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence,
		        pii_flag, pii_pattern, excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE workspace_id = $1 ORDER BY dataset_id, name ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing workspace columns: %w", err)
	}
	defer rows.Close()
	return scanColumns(rows)
}

// scanColumns scans every row of a `columns` query sharing the five-statistic column list above.
func scanColumns(rows pgx.Rows) ([]domain.Column, error) {
	var out []domain.Column
	for rows.Next() {
		var (
			c            domain.Column
			inferredType string
		)
		if err := rows.Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence,
			&c.PIIFlag, &c.PIIPattern, &c.ExcludedFromModel, &c.NullRate, &c.DistinctCount); err != nil {
			return nil, fmt.Errorf("scanning column: %w", err)
		}
		c.InferredType = domain.InferredType(inferredType)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating columns: %w", err)
	}
	return out, nil
}

// GetByID returns the column with id, or ErrColumnNotFound.
func (r *ColumnRepository) GetByID(ctx context.Context, id string) (domain.Column, error) {
	var (
		c            domain.Column
		inferredType string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence,
		        pii_flag, pii_pattern, excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE id = $1`,
		id,
	).Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence,
		&c.PIIFlag, &c.PIIPattern, &c.ExcludedFromModel, &c.NullRate, &c.DistinctCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Column{}, ErrColumnNotFound
		}
		return domain.Column{}, fmt.Errorf("getting column: %w", err)
	}
	c.InferredType = domain.InferredType(inferredType)
	return c, nil
}

// GetByIDForWorkspace returns the column with id, scoped to workspaceID, or ErrColumnNotFound if it
// doesn't exist or belongs to a different workspace. The same "not found" response covers both
// cases — screens/07-ingest-confirmation.md's review/override endpoints (FEAT-006/FEAT-007) must
// never confirm that a column id exists in some OTHER workspace the caller doesn't own, matching this
// API's existing non-enumeration pattern (see handleListColumns's doc comment).
func (r *ColumnRepository) GetByIDForWorkspace(ctx context.Context, id, workspaceID string) (domain.Column, error) {
	var (
		c            domain.Column
		inferredType string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, dataset_id, name, inferred_type, type_confidence,
		        pii_flag, pii_pattern, excluded_from_model, null_rate, distinct_count
		 FROM columns WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	).Scan(&c.ID, &c.DatasetID, &c.Name, &inferredType, &c.TypeConfidence,
		&c.PIIFlag, &c.PIIPattern, &c.ExcludedFromModel, &c.NullRate, &c.DistinctCount)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Column{}, ErrColumnNotFound
		}
		return domain.Column{}, fmt.Errorf("getting column for workspace: %w", err)
	}
	c.InferredType = domain.InferredType(inferredType)
	return c, nil
}

// UpdateInferredType overrides a column's inferred type per FEAT-006 — the user's explicit review
// decision on screens/07-ingest-confirmation.md. type_confidence is set to 1.0: this is now a
// human-confirmed value, not a heuristic guess, and every downstream reader of type_confidence
// (including relationship inference's type-compatibility signal) should treat it accordingly.
func (r *ColumnRepository) UpdateInferredType(ctx context.Context, id string, t domain.InferredType) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE columns SET inferred_type = $2, type_confidence = 1.0 WHERE id = $1`,
		id, string(t),
	)
	if err != nil {
		return fmt.Errorf("updating column inferred type: %w", err)
	}
	return nil
}

// UpdatePII overrides a column's PII flag and detected pattern per FEAT-007 — the user's explicit
// review decision on screens/07-ingest-confirmation.md. pattern is caller-resolved: empty when
// clearing a flag (schema.md's Column.pii_pattern is "empty string if not flagged"), or a specific
// pattern name when setting one — either the pattern DetectAll originally proposed, re-confirmed, or
// "manual" when the user flags a column DetectAll did not.
func (r *ColumnRepository) UpdatePII(ctx context.Context, id string, flag bool, pattern string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE columns SET pii_flag = $2, pii_pattern = $3 WHERE id = $1`,
		id, flag, pattern,
	)
	if err != nil {
		return fmt.Errorf("updating column pii flag: %w", err)
	}
	return nil
}

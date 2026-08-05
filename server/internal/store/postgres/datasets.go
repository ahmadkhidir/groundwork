package postgres

import (
	"context"
	"fmt"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// DatasetRepository persists and retrieves datasets.
//
// This wave (FEAT-003) only creates rows at upload time, with ingest_status "pending" and an empty
// parquet_location — normalization into the canonical Parquet form is FEAT-004's job (Wave 3, the
// Engine Provider seam). This repository intentionally does not yet expose an update path for that
// reason; Wave 3 adds it alongside the ingest pipeline that needs it.
type DatasetRepository struct {
	pool *Pool
}

// NewDatasetRepository constructs a DatasetRepository backed by pool.
func NewDatasetRepository(pool *Pool) *DatasetRepository {
	return &DatasetRepository{pool: pool}
}

// Create inserts a new dataset row.
func (r *DatasetRepository) Create(ctx context.Context, ds domain.Dataset) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO datasets
		   (id, workspace_id, original_filename, format, parquet_location, ingest_status, row_count, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		ds.ID, ds.WorkspaceID, ds.OriginalFilename, string(ds.Format),
		ds.ParquetLocation, string(ds.IngestStatus), ds.RowCount, ds.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting dataset: %w", err)
	}
	return nil
}

// ListByWorkspace returns every dataset in workspaceID, most recently uploaded first.
func (r *DatasetRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, original_filename, format, parquet_location, ingest_status, row_count, created_at
		 FROM datasets WHERE workspace_id = $1 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing datasets: %w", err)
	}
	defer rows.Close()

	var out []domain.Dataset
	for rows.Next() {
		var (
			ds     domain.Dataset
			format string
			status string
		)
		if err := rows.Scan(&ds.ID, &ds.WorkspaceID, &ds.OriginalFilename, &format,
			&ds.ParquetLocation, &status, &ds.RowCount, &ds.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning dataset: %w", err)
		}
		ds.Format = domain.DatasetFormat(format)
		ds.IngestStatus = domain.IngestStatus(status)
		out = append(out, ds)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating datasets: %w", err)
	}
	return out, nil
}

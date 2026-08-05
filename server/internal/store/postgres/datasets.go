package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrDatasetNotFound is returned when no dataset matches the given id.
var ErrDatasetNotFound = errors.New("dataset not found")

// DatasetRepository persists and retrieves datasets.
//
// FEAT-003 (Wave 2) only created rows at upload time, with ingest_status "pending" and an empty
// parquet_location. FEAT-004 (Wave 3, this file's ingest-state extension) adds the update path the
// Ingest Pipeline needs to move a dataset through provisioning -> profiling (handoff to FEAT-005) or
// -> failed, and to persist the normalized Parquet location and row count once loading completes.
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

// GetByID returns the dataset with id, or ErrDatasetNotFound.
func (r *DatasetRepository) GetByID(ctx context.Context, id string) (domain.Dataset, error) {
	var (
		ds     domain.Dataset
		format string
		status string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, original_filename, format, parquet_location, ingest_status, row_count, created_at
		 FROM datasets WHERE id = $1`,
		id,
	).Scan(&ds.ID, &ds.WorkspaceID, &ds.OriginalFilename, &format,
		&ds.ParquetLocation, &status, &ds.RowCount, &ds.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Dataset{}, ErrDatasetNotFound
		}
		return domain.Dataset{}, fmt.Errorf("getting dataset: %w", err)
	}
	ds.Format = domain.DatasetFormat(format)
	ds.IngestStatus = domain.IngestStatus(status)
	return ds, nil
}

// ListInProgressByWorkspace returns every dataset in workspaceID whose ingest has not reached a
// successful terminal state — the batch the Ingest Progress screen (screens/06) polls. "failed" is
// deliberately included (not just pending/provisioning/profiling): the screen's Ingest Error state
// needs to keep showing a failed dataset's specific reason, not have it silently drop off the list.
func (r *DatasetRepository) ListInProgressByWorkspace(ctx context.Context, workspaceID string) ([]domain.Dataset, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, original_filename, format, parquet_location, ingest_status, row_count, created_at
		 FROM datasets
		 WHERE workspace_id = $1 AND ingest_status != 'complete'
		 ORDER BY created_at ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing in-progress datasets: %w", err)
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
		return nil, fmt.Errorf("iterating in-progress datasets: %w", err)
	}
	return out, nil
}

// IngestErrorByID returns the ingest_error column for id — the specific, actionable failure reason
// screens/06-ingest-progress.md's Ingest Error state requires. Kept out of domain.Dataset (see
// migration 00003's comment) rather than added to the read-only schema.md-mirrored struct.
func (r *DatasetRepository) IngestErrorByID(ctx context.Context, id string) (string, error) {
	var reason string
	err := r.pool.QueryRow(ctx, `SELECT ingest_error FROM datasets WHERE id = $1`, id).Scan(&reason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrDatasetNotFound
		}
		return "", fmt.Errorf("getting ingest error: %w", err)
	}
	return reason, nil
}

// SetIngestStatus transitions a dataset's ingest_status, clearing any previously recorded error —
// used for the pending -> provisioning transition, which always starts clean.
func (r *DatasetRepository) SetIngestStatus(ctx context.Context, id string, status domain.IngestStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE datasets SET ingest_status = $2, ingest_error = '' WHERE id = $1`,
		id, string(status),
	)
	if err != nil {
		return fmt.Errorf("updating ingest status: %w", err)
	}
	return nil
}

// CompleteLoad records a successfully normalized, durably-stored dataset and hands ingest_status off
// to "profiling" — FEAT-005's stage (Wave 4), not this one's. FEAT-004's job ends the moment the
// normalized Parquet is durable and independent of the engine's lifecycle (its acceptance criterion);
// it does not claim "complete", which schema.md reserves for the end of the whole pipeline.
func (r *DatasetRepository) CompleteLoad(ctx context.Context, id, parquetLocation string, rowCount int64) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE datasets
		 SET parquet_location = $2, row_count = $3, ingest_status = $4, ingest_error = ''
		 WHERE id = $1`,
		id, parquetLocation, rowCount, string(domain.IngestProfiling),
	)
	if err != nil {
		return fmt.Errorf("completing dataset load: %w", err)
	}
	return nil
}

// CompleteIngest marks a dataset's ingest_status "complete" — the true end of the whole pipeline,
// reached once FEAT-005 (profiling) and, when applicable, FEAT-008 (relationship inference) have both
// finished for this dataset. Added by Wave 4 to close the open item left by FEAT-004 (Wave 3):
// CompleteLoad above deliberately stops at "profiling," which is not schema.md's terminal state.
func (r *DatasetRepository) CompleteIngest(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE datasets SET ingest_status = $2, ingest_error = '' WHERE id = $1`,
		id, string(domain.IngestComplete),
	)
	if err != nil {
		return fmt.Errorf("completing ingest: %w", err)
	}
	return nil
}

// MarkFailed records a specific, actionable ingest failure reason and moves the dataset to its
// terminal "failed" state — FEAT-004's acceptance criterion "Ingest failure surfaces a specific,
// actionable error."
func (r *DatasetRepository) MarkFailed(ctx context.Context, id, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE datasets SET ingest_status = $2, ingest_error = $3 WHERE id = $1`,
		id, string(domain.IngestFailed), reason,
	)
	if err != nil {
		return fmt.Errorf("marking dataset failed: %w", err)
	}
	return nil
}

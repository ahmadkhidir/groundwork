package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrCandidateJoinNotFound is returned when no candidate join matches the given id.
var ErrCandidateJoinNotFound = errors.New("candidate join not found")

// CandidateJoinRepository persists and retrieves candidate joins proposed by relationship inference
// (FEAT-008), read back by the ingest confirmation review flow (FEAT-009). Every row starts
// "pending" — schema.md's CandidateJoin is explicit that a join is "Never auto-applied."
type CandidateJoinRepository struct {
	pool *Pool
}

// NewCandidateJoinRepository constructs a CandidateJoinRepository backed by pool.
func NewCandidateJoinRepository(pool *Pool) *CandidateJoinRepository {
	return &CandidateJoinRepository{pool: pool}
}

// Create inserts a new candidate join row.
func (r *CandidateJoinRepository) Create(ctx context.Context, cj domain.CandidateJoin) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO candidate_joins
		   (id, workspace_id, source_column_id, target_column_id, confidence_score, basis, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		cj.ID, cj.WorkspaceID, cj.SourceColumnID, cj.TargetColumnID, cj.ConfidenceScore, cj.Basis,
		string(cj.Status), cj.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting candidate join: %w", err)
	}
	return nil
}

// ListByWorkspace returns every candidate join in workspaceID, most recently inferred first.
func (r *CandidateJoinRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.CandidateJoin, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, source_column_id, target_column_id, confidence_score, basis, status, created_at
		 FROM candidate_joins WHERE workspace_id = $1 ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing candidate joins: %w", err)
	}
	defer rows.Close()

	var out []domain.CandidateJoin
	for rows.Next() {
		var (
			cj     domain.CandidateJoin
			status string
		)
		if err := rows.Scan(&cj.ID, &cj.WorkspaceID, &cj.SourceColumnID, &cj.TargetColumnID,
			&cj.ConfidenceScore, &cj.Basis, &status, &cj.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning candidate join: %w", err)
		}
		cj.Status = domain.CandidateJoinStatus(status)
		out = append(out, cj)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating candidate joins: %w", err)
	}
	return out, nil
}

// ExistsForPair reports whether a candidate join already exists between columnAID and columnBID, in
// either direction, regardless of status. Relationship inference checks this before persisting a new
// candidate so that (a) a re-run never inserts a duplicate row for the same pair and (b) a join the
// user already rejected is "never silently reintroduced on a later session" (FEAT-009's acceptance
// criterion) merely because ingest ran again over the same files.
func (r *CandidateJoinRepository) ExistsForPair(ctx context.Context, columnAID, columnBID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM candidate_joins
			WHERE (source_column_id = $1 AND target_column_id = $2)
			   OR (source_column_id = $2 AND target_column_id = $1)
		)`,
		columnAID, columnBID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking candidate join existence: %w", err)
	}
	return exists, nil
}

// UpdateStatus transitions a candidate join to "accepted" or "rejected" per FEAT-009 — the user's
// explicit accept/reject decision on screens/07-ingest-confirmation.md. Never used to set "pending":
// that value is only ever assigned at creation time by relationship inference.
func (r *CandidateJoinRepository) UpdateStatus(ctx context.Context, id string, status domain.CandidateJoinStatus) error {
	_, err := r.pool.Exec(ctx, `UPDATE candidate_joins SET status = $2 WHERE id = $1`, id, string(status))
	if err != nil {
		return fmt.Errorf("updating candidate join status: %w", err)
	}
	return nil
}

// ExistsRejectedForNamePair reports whether a candidate join between two columns identified by
// (dataset original filename, column name) — in either direction — was ever rejected in workspaceID.
//
// Deliberately name-based rather than id-based, unlike ExistsForPair: a later ingest of "the same
// files" (FEAT-009's own phrasing) creates entirely NEW Dataset and Column rows with new ids, so an
// id-keyed check alone would silently forget a rejection the moment the user re-uploads. Matching by
// filename+column name is what makes FEAT-009's "never silently reintroduced... with the same files"
// guarantee survive a re-ingest, not merely a single upload session.
func (r *CandidateJoinRepository) ExistsRejectedForNamePair(ctx context.Context, workspaceID, filenameA, columnA, filenameB, columnB string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM candidate_joins cj
			JOIN columns sc ON sc.id = cj.source_column_id
			JOIN columns tc ON tc.id = cj.target_column_id
			JOIN datasets sd ON sd.id = sc.dataset_id
			JOIN datasets td ON td.id = tc.dataset_id
			WHERE cj.workspace_id = $1
			  AND cj.status = 'rejected'
			  AND (
				(sd.original_filename = $2 AND sc.name = $3 AND td.original_filename = $4 AND tc.name = $5)
				OR
				(sd.original_filename = $4 AND sc.name = $5 AND td.original_filename = $2 AND tc.name = $3)
			  )
		)`,
		workspaceID, filenameA, columnA, filenameB, columnB,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking rejected candidate join by name: %w", err)
	}
	return exists, nil
}

// GetByID returns the candidate join with id, or ErrCandidateJoinNotFound.
func (r *CandidateJoinRepository) GetByID(ctx context.Context, id string) (domain.CandidateJoin, error) {
	var (
		cj     domain.CandidateJoin
		status string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, source_column_id, target_column_id, confidence_score, basis, status, created_at
		 FROM candidate_joins WHERE id = $1`,
		id,
	).Scan(&cj.ID, &cj.WorkspaceID, &cj.SourceColumnID, &cj.TargetColumnID,
		&cj.ConfidenceScore, &cj.Basis, &status, &cj.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CandidateJoin{}, ErrCandidateJoinNotFound
		}
		return domain.CandidateJoin{}, fmt.Errorf("getting candidate join: %w", err)
	}
	cj.Status = domain.CandidateJoinStatus(status)
	return cj, nil
}

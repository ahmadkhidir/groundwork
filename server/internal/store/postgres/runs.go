package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrRunNotFound is returned when no run matches the given id.
var ErrRunNotFound = errors.New("run not found")

// RunRepository persists and retrieves runs (FEAT-013, Phase 2).
//
// A run row is created by the API handler with status "queued"; every later transition — waking,
// running, and the terminal complete/failed states — is written by internal/run's linear executor
// (the only component allowed to advance run state after creation), which is what keeps "the UI is
// a viewer attached to server-side state" honest: no HTTP handler reports a transition it did not
// itself persist.
type RunRepository struct {
	pool *Pool
}

// NewRunRepository constructs a RunRepository backed by pool.
func NewRunRepository(pool *Pool) *RunRepository {
	return &RunRepository{pool: pool}
}

// Create inserts a new run row.
//
// The caller sets Status (the API handler always creates "queued"); parent_run_id follows schema.md's
// empty-string-means-absent convention, so a fork-less run stores NULL rather than "".
func (r *RunRepository) Create(ctx context.Context, run domain.Run) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO runs (id, workspace_id, question_text, status, parent_run_id, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		run.ID, run.WorkspaceID, run.QuestionText, string(run.Status),
		nullableString(run.ParentRunID), run.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("inserting run: %w", err)
	}
	return nil
}

// GetByID returns the run with id, or ErrRunNotFound.
func (r *RunRepository) GetByID(ctx context.Context, id string) (domain.Run, error) {
	var (
		run         domain.Run
		status      string
		parentRunID *string
		completedAt *time.Time
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, workspace_id, question_text, status, parent_run_id, created_at, completed_at
		 FROM runs WHERE id = $1`,
		id,
	).Scan(&run.ID, &run.WorkspaceID, &run.QuestionText, &status, &parentRunID, &run.CreatedAt, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Run{}, ErrRunNotFound
		}
		return domain.Run{}, fmt.Errorf("getting run: %w", err)
	}
	run.Status = domain.RunStatus(status)
	if parentRunID != nil {
		run.ParentRunID = *parentRunID
	}
	run.CompletedAt = completedAt
	return run, nil
}

// UpdateStatus advances a run's status.
//
// completed_at is stamped in the SAME statement as the transition to a terminal status (complete,
// failed, or halted): a run that reads "complete" with no completion time, or a time written by a
// separate best-effort update that could drift from the transition, would be two sources of truth.
// Non-terminal transitions leave completed_at untouched so a run can never be resurrected past its
// terminal state by a stray transition.
func (r *RunRepository) UpdateStatus(ctx context.Context, id string, status domain.RunStatus) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE runs
		    SET status = $2,
		        completed_at = CASE WHEN $2 IN ('complete', 'failed', 'halted') THEN now() ELSE completed_at END
		  WHERE id = $1`,
		id, string(status),
	)
	if err != nil {
		return fmt.Errorf("updating run status: %w", err)
	}
	return nil
}

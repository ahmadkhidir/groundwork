package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrStepNotFound is returned when no step matches the given id.
var ErrStepNotFound = errors.New("step not found")

// StepRepository persists and retrieves individual DAG steps (FEAT-013, Phase 2).
//
// Steps are created by internal/run's executor in status "running" the moment their work is about
// to begin — a step row existing at all means work is (or just finished being) performed, so
// "pending" has no honest window in this wave's linear executor. The status transitions that follow
// (complete/failed) are single-statement updates that carry the step's output and completion time.
type StepRepository struct {
	pool *Pool
}

// NewStepRepository constructs a StepRepository backed by pool.
func NewStepRepository(pool *Pool) *StepRepository {
	return &StepRepository{pool: pool}
}

// Create inserts a new step row for run runID within workspaceID, with started_at stamped now.
//
// workspaceID is a separate parameter rather than a field on domain.Step — the same denormalization
// decision documented on columns.go's BulkInsert: schema.md's Step has no workspace_id property, but
// the table carries it (see migration 00001's comment on steps.workspace_id) so workspace-scoped
// queries never join through runs. parent_step_id follows the empty-string-means-absent convention.
func (r *StepRepository) Create(ctx context.Context, workspaceID string, step domain.Step) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO steps
		   (id, run_id, workspace_id, parent_step_id, step_name, status, input, output,
		    idempotency_key, started_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, now())`,
		step.ID, step.RunID, workspaceID, nullableString(step.ParentStepID),
		string(step.StepName), string(step.Status), step.Input, step.Output, step.IdempotencyKey,
	)
	if err != nil {
		return fmt.Errorf("inserting step: %w", err)
	}
	return nil
}

// ListByRun returns every step belonging to runID in creation order.
//
// Creation order is the pipeline order this wave's linear executor writes steps in (plan, then
// generate_sql, then execute); ordering by created_at, id keeps the tiebreak deterministic so a
// re-read of a run always renders the same feed.
func (r *StepRepository) ListByRun(ctx context.Context, runID string) ([]domain.Step, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, run_id, parent_step_id, step_name, status, input, output, idempotency_key,
		        started_at, completed_at
		 FROM steps WHERE run_id = $1 ORDER BY created_at ASC, id ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing steps: %w", err)
	}
	defer rows.Close()
	return scanSteps(rows)
}

// GetByID returns the step with id, or ErrStepNotFound.
func (r *StepRepository) GetByID(ctx context.Context, id string) (domain.Step, error) {
	var (
		step         domain.Step
		parentStepID *string
	)
	err := r.pool.QueryRow(ctx,
		`SELECT id, run_id, parent_step_id, step_name, status, input, output, idempotency_key,
		        started_at, completed_at
		 FROM steps WHERE id = $1`,
		id,
	).Scan(&step.ID, &step.RunID, &parentStepID, &step.StepName, &step.Status,
		&step.Input, &step.Output, &step.IdempotencyKey, &step.StartedAt, &step.CompletedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Step{}, ErrStepNotFound
		}
		return domain.Step{}, fmt.Errorf("getting step: %w", err)
	}
	if parentStepID != nil {
		step.ParentStepID = *parentStepID
	}
	return step, nil
}

// Complete marks a step complete, persisting its output and completion time in one statement.
//
// output is the JSON-encoded step payload (domain/run.go's shapes) — the same bytes the model
// produced, so re-reading a completed step replays exactly what happened.
func (r *StepRepository) Complete(ctx context.Context, id, output string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE steps SET status = 'complete', output = $2, completed_at = now() WHERE id = $1`,
		id, output,
	)
	if err != nil {
		return fmt.Errorf("completing step: %w", err)
	}
	return nil
}

// Fail marks a step failed, persisting a reason and completion time in one statement.
//
// reason is the plain-text error message, not a JSON payload: a failed step produced no valid output
// of its step_name's shape, and pretending otherwise would corrupt the "output replays what happened"
// contract above. The Run Viewer renders it verbatim.
func (r *StepRepository) Fail(ctx context.Context, id, reason string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE steps SET status = 'failed', output = $2, completed_at = now() WHERE id = $1`,
		id, reason,
	)
	if err != nil {
		return fmt.Errorf("failing step: %w", err)
	}
	return nil
}

// scanSteps scans every row of a `steps` query sharing the column list above.
func scanSteps(rows pgx.Rows) ([]domain.Step, error) {
	var out []domain.Step
	for rows.Next() {
		var (
			step         domain.Step
			parentStepID *string
		)
		if err := rows.Scan(&step.ID, &step.RunID, &parentStepID, &step.StepName, &step.Status,
			&step.Input, &step.Output, &step.IdempotencyKey, &step.StartedAt, &step.CompletedAt); err != nil {
			return nil, fmt.Errorf("scanning step: %w", err)
		}
		if parentStepID != nil {
			step.ParentStepID = *parentStepID
		}
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating steps: %w", err)
	}
	return out, nil
}

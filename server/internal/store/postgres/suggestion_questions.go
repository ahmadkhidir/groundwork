package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// ErrSuggestionQuestionNotFound is returned when no suggestion row matches the given id (or the
// requesting workspace does not own it — the two are made indistinguishable at the API layer so a
// non-owner cannot probe for the existence of a row that is not theirs).
var ErrSuggestionQuestionNotFound = errors.New("suggestion question not found")

// SuggestionQuestionRepository persists a workspace's model-generated question suggestions
// (FEAT-051's "add to the list" behavior). The deterministic strip is computed at read time by
// suggestions.Generate and never touches this table.
type SuggestionQuestionRepository struct {
	pool *Pool
}

// NewSuggestionQuestionRepository constructs a SuggestionQuestionRepository backed by pool.
func NewSuggestionQuestionRepository(pool *Pool) *SuggestionQuestionRepository {
	return &SuggestionQuestionRepository{pool: pool}
}

// ListByWorkspace returns every persisted suggestion for the workspace, oldest first — the order
// the UI renders the accumulated bucket in (append order, so the newest suggestions are at the
// end).
func (r *SuggestionQuestionRepository) ListByWorkspace(ctx context.Context, workspaceID string) ([]domain.SuggestionQuestion, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, workspace_id, question, created_at
		 FROM suggestion_questions
		 WHERE workspace_id = $1
		 ORDER BY created_at ASC, id ASC`,
		workspaceID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing suggestion questions: %w", err)
	}
	defer rows.Close()

	var out []domain.SuggestionQuestion
	for rows.Next() {
		var s domain.SuggestionQuestion
		if err := rows.Scan(&s.ID, &s.WorkspaceID, &s.Question, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning suggestion question: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating suggestion questions: %w", err)
	}
	return out, nil
}

// Add inserts each question the handler already deduplicated against the deterministic set and the
// existing bucket, then returns the full updated bucket (oldest first). Duplicate inserts are
// swallowed by the UNIQUE (workspace_id, question) constraint rather than failing the request — a
// concurrent generation racing this one must not error the run.
func (r *SuggestionQuestionRepository) Add(ctx context.Context, workspaceID string, questions []string) ([]domain.SuggestionQuestion, error) {
	for _, q := range questions {
		if q == "" {
			continue
		}
		_, err := r.pool.Exec(ctx,
			`INSERT INTO suggestion_questions (id, workspace_id, question, created_at)
			 VALUES ($1, $2, $3, now())
			 ON CONFLICT (workspace_id, question) DO NOTHING`,
			uuid.NewString(), workspaceID, q,
		)
		if err != nil {
			return nil, fmt.Errorf("inserting suggestion question: %w", err)
		}
	}
	return r.ListByWorkspace(ctx, workspaceID)
}

// Delete removes one suggestion from the workspace. Returns ErrSuggestionQuestionNotFound when no
// row matches — whether it never existed or belongs to a different workspace.
func (r *SuggestionQuestionRepository) Delete(ctx context.Context, workspaceID, id string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM suggestion_questions WHERE id = $1 AND workspace_id = $2`,
		id, workspaceID,
	)
	if err != nil {
		return fmt.Errorf("deleting suggestion question: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrSuggestionQuestionNotFound
	}
	return nil
}

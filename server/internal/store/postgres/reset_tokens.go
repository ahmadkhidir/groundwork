package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
)

// ResetTokenRepository is the durable auth.ResetTokenStore implementation, backed by the
// password_reset_tokens table (migration 00011, schema.md's PasswordResetToken entity).
//
// Tokens are stored as one-way hashes; the raw token exists only in the delivered link. Rows are
// durable — a reset link keeps working across a restart or multiple API instances, per
// architecture.md. This replaces the interim in-memory store as the production wiring.
type ResetTokenRepository struct {
	pool *Pool
}

// NewResetTokenRepository constructs a ResetTokenRepository backed by pool.
func NewResetTokenRepository(pool *Pool) *ResetTokenRepository {
	return &ResetTokenRepository{pool: pool}
}

// Issue persists a token hash for userID, expiring at expiresAt.
func (r *ResetTokenRepository) Issue(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (id, user_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		uuid.NewString(), userID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("inserting password reset token: %w", err)
	}
	return nil
}

// Consume validates tokenHash, marks it consumed, and — on success — invalidates the user's other
// outstanding tokens (screens/19 step 5). All in one transaction.
//
// Single-use regardless of validity: the row is marked consumed even for an expired or
// already-consumed token, so a leaked/observed token cannot be replayed after ANY attempt. The
// SELECT ... FOR UPDATE serializes concurrent attempts on the same token so exactly one wins.
func (r *ResetTokenRepository) Consume(ctx context.Context, tokenHash string) (string, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id         string
		userID     string
		expiresAt  time.Time
		consumedAt *time.Time
	)
	err = tx.QueryRow(ctx,
		`SELECT id, user_id, expires_at, consumed_at
		 FROM password_reset_tokens
		 WHERE token_hash = $1
		 FOR UPDATE`,
		tokenHash,
	).Scan(&id, &userID, &expiresAt, &consumedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", auth.ErrResetTokenNotFound
		}
		return "", fmt.Errorf("looking up password reset token: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET consumed_at = now() WHERE id = $1`, id,
	); err != nil {
		return "", fmt.Errorf("consuming password reset token: %w", err)
	}

	if consumedAt != nil || time.Now().After(expiresAt) {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return "", auth.ErrResetTokenNotFound
	}

	// On success, invalidate the user's other outstanding tokens (screens/19 step 5).
	if _, err := tx.Exec(ctx,
		`UPDATE password_reset_tokens SET consumed_at = now()
		 WHERE user_id = $1 AND id <> $2 AND consumed_at IS NULL`,
		userID, id,
	); err != nil {
		return "", fmt.Errorf("invalidating other password reset tokens: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return userID, nil
}

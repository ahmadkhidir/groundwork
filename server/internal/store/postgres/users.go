package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// uniqueViolationCode is Postgres's SQLSTATE for a unique-constraint violation.
const uniqueViolationCode = "23505"

// UserRepository persists and retrieves users, satisfying auth.UserStore.
//
// The password hash is intentionally NOT part of domain.User (schema.md excludes it from the User
// entity — see migrations/00001_init.sql), so every method here returns it as a separate string
// rather than a field a caller could accidentally serialize into a client response.
type UserRepository struct {
	pool *Pool
}

// NewUserRepository constructs a UserRepository backed by pool.
func NewUserRepository(pool *Pool) *UserRepository {
	return &UserRepository{pool: pool}
}

// Create inserts a new user with the given (already-hashed) password.
func (r *UserRepository) Create(ctx context.Context, user domain.User, passwordHash string) error {
	_, err := r.pool.Exec(ctx,
		`INSERT INTO users (id, email, display_name, password_hash, tier, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		user.ID, user.Email, user.DisplayName, passwordHash, string(user.Tier), user.CreatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return auth.ErrEmailTaken
		}
		return fmt.Errorf("inserting user: %w", err)
	}
	return nil
}

// FindByEmail returns the user and password hash for email, or auth.ErrUserNotFound.
func (r *UserRepository) FindByEmail(ctx context.Context, email string) (domain.User, string, error) {
	return r.find(ctx, "email = $1", email)
}

// FindByID returns the user and password hash for id, or auth.ErrUserNotFound.
func (r *UserRepository) FindByID(ctx context.Context, id string) (domain.User, string, error) {
	return r.find(ctx, "id = $1", id)
}

func (r *UserRepository) find(ctx context.Context, whereClause, arg string) (domain.User, string, error) {
	var (
		user domain.User
		tier string
		hash string
	)

	err := r.pool.QueryRow(ctx,
		`SELECT id, email, display_name, password_hash, tier, created_at
		 FROM users WHERE `+whereClause,
		arg,
	).Scan(&user.ID, &user.Email, &user.DisplayName, &hash, &tier, &user.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.User{}, "", auth.ErrUserNotFound
		}
		return domain.User{}, "", fmt.Errorf("querying user: %w", err)
	}

	user.Tier = domain.SubscriptionTier(tier)
	return user, hash, nil
}

// UpdatePasswordHash overwrites the stored hash for userID — used by the password-reset confirm
// step. Returns auth.ErrUserNotFound if the user no longer exists.
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash = $1 WHERE id = $2`,
		passwordHash, userID,
	)
	if err != nil {
		return fmt.Errorf("updating password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrUserNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode
}

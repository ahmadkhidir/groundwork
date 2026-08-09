package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

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

// UserBilling is the Stripe-facing billing state of a user's row: the authoritative tier plus the
// Stripe customer/subscription ids the webhook handlers and the subscription read endpoint need.
//
// It deliberately lives in the store rather than on domain.User because schema.md's User entity has
// no Stripe fields — see migrations/00010_stripe_billing.sql for the schema addition the docs do not
// model. Empty strings follow the codebase's empty-string-means-absent convention; a zero
// StripeSubscriptionPeriodEnd means no current billing period is stored.
type UserBilling struct {
	UserID                      string
	Email                       string
	Tier                        domain.SubscriptionTier
	StripeCustomerID            string
	StripeSubscriptionID        string
	StripeSubscriptionStatus    string
	StripeSubscriptionPeriodEnd time.Time
}

// UpdateTier overwrites the user's authoritative subscription tier. The ONLY writer is the billing
// webhook path (upgrade via checkout.session.completed, revert to free via
// customer.subscription.deleted, per monetization.md's Billing Events) — no feature slice calls it.
// Returns auth.ErrUserNotFound if the user no longer exists.
func (r *UserRepository) UpdateTier(ctx context.Context, userID string, tier domain.SubscriptionTier) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET tier = $1 WHERE id = $2`,
		string(tier), userID,
	)
	if err != nil {
		return fmt.Errorf("updating user tier: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrUserNotFound
	}
	return nil
}

// SetStripeCustomer records the Stripe customer id Stripe assigned during checkout, so the webhook
// handlers can reconcile subscription events (which carry a customer id, not a user id) back to the
// account. Returns auth.ErrUserNotFound if the user no longer exists.
func (r *UserRepository) SetStripeCustomer(ctx context.Context, userID, customerID string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET stripe_customer_id = $1 WHERE id = $2`,
		customerID, userID,
	)
	if err != nil {
		return fmt.Errorf("setting stripe customer: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrUserNotFound
	}
	return nil
}

// SetStripeSubscription records the user's active Stripe subscription (id, status, and period end)
// as of the latest webhook. An empty subscriptionID CLEARS the subscription state — cancellation or
// final payment failure (customer.subscription.deleted) — and a zero periodEnd stores NULL (no
// current period). Status is Stripe's own value ("active", "past_due", "trialing", ...), surfaced
// verbatim by the subscription read endpoint. Returns auth.ErrUserNotFound if the user is gone.
func (r *UserRepository) SetStripeSubscription(ctx context.Context, userID, subscriptionID, status string, periodEnd time.Time) error {
	var periodEndArg any
	if !periodEnd.IsZero() {
		periodEndArg = periodEnd
	}
	tag, err := r.pool.Exec(ctx,
		`UPDATE users
		 SET stripe_subscription_id = $1,
		     stripe_subscription_status = $2,
		     stripe_subscription_period_end = $3
		 WHERE id = $4`,
		subscriptionID, status, periodEndArg, userID,
	)
	if err != nil {
		return fmt.Errorf("setting stripe subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrUserNotFound
	}
	return nil
}

// GetBillingByID returns the user's billing state, or auth.ErrUserNotFound.
func (r *UserRepository) GetBillingByID(ctx context.Context, userID string) (UserBilling, error) {
	return r.findBilling(ctx, "id = $1", userID)
}

// GetBillingByStripeCustomerID finds the user who owns customerID, or auth.ErrUserNotFound. This is
// how subscription-scoped webhook events (customer.subscription.deleted, ...) are reconciled back to
// an account, since Stripe events carry the customer id and never a user id.
func (r *UserRepository) GetBillingByStripeCustomerID(ctx context.Context, customerID string) (UserBilling, error) {
	return r.findBilling(ctx, "stripe_customer_id = $1", customerID)
}

// Delete removes a user row (FEAT-052's account deletion — the LAST step of the cascade, after every
// owned workspace and user-scoped row is gone). Hard delete: schema.md deliberately introduces no
// soft-delete. Returns auth.ErrUserNotFound when the row no longer exists (a concurrent deletion won
// the race — the cascade is idempotent and safe to retry, but there is nothing left to delete).
func (r *UserRepository) Delete(ctx context.Context, userID string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrUserNotFound
	}
	return nil
}

func (r *UserRepository) findBilling(ctx context.Context, whereClause, arg string) (UserBilling, error) {
	var (
		billing   UserBilling
		tier      string
		periodEnd *time.Time
	)

	err := r.pool.QueryRow(ctx,
		`SELECT id, email, tier, stripe_customer_id, stripe_subscription_id, stripe_subscription_status,
		        stripe_subscription_period_end
		 FROM users WHERE `+whereClause,
		arg,
	).Scan(&billing.UserID, &billing.Email, &tier, &billing.StripeCustomerID, &billing.StripeSubscriptionID,
		&billing.StripeSubscriptionStatus, &periodEnd)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return UserBilling{}, auth.ErrUserNotFound
		}
		return UserBilling{}, fmt.Errorf("querying user billing: %w", err)
	}

	billing.Tier = domain.SubscriptionTier(tier)
	if periodEnd != nil {
		billing.StripeSubscriptionPeriodEnd = *periodEnd
	}
	return billing, nil
}

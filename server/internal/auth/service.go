package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// Sentinel errors returned by Service. Handlers in internal/api map these to HTTP responses.
var (
	// ErrUserNotFound is returned by a UserStore when no row matches. Service never lets it reach a
	// login caller directly — see Authenticate.
	ErrUserNotFound = errors.New("user not found")
	// ErrEmailTaken is returned by SignUp when the email is already registered.
	ErrEmailTaken = errors.New("email already registered")
	// ErrInvalidCredentials is returned by Authenticate for BOTH "no such account" and "wrong
	// password" — see Authenticate's doc comment for why the two must not be distinguishable.
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrInvalidResetToken is returned by ConfirmPasswordReset for a missing, already-used, or
	// expired token.
	ErrInvalidResetToken = errors.New("invalid or expired reset token")
)

// UserStore is the persistence seam Service depends on, satisfied by
// internal/store/postgres.UserRepository. Defined here, as a consumer-side interface, so this
// package can be unit-tested against a fake without importing the Postgres driver.
type UserStore interface {
	// Create inserts a new user with the given (already-hashed) password. Returns ErrEmailTaken if
	// the email is already registered.
	Create(ctx context.Context, user domain.User, passwordHash string) error
	// FindByEmail returns the user and its stored password hash, or ErrUserNotFound.
	FindByEmail(ctx context.Context, email string) (domain.User, string, error)
	// FindByID returns the user and its stored password hash, or ErrUserNotFound.
	FindByID(ctx context.Context, id string) (domain.User, string, error)
	// UpdatePasswordHash overwrites the stored hash for userID. Returns ErrUserNotFound if the user
	// no longer exists.
	UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error
}

// LoginAuditor writes the account-scoped `login` entry to the combined audit/egress log
// (FEAT-042), satisfied by internal/audit.Writer.
//
// Defined consumer-side, matching UserStore's shape, so this package stays unit-testable without
// importing internal/audit or internal/store/postgres.
type LoginAuditor interface {
	WriteLogin(ctx context.Context, userID string) error
}

// Service implements FEAT-001: account creation, credential verification, and password reset.
type Service struct {
	users  UserStore
	resets ResetTokenStore
	logger *slog.Logger
	// loginAuditor is nil until SetLoginAuditor is called. Kept optional (rather than a required
	// constructor argument) so every existing caller of NewService — including this package's own
	// tests and internal/api's — keeps compiling unchanged; cmd/api/main.go is the one production
	// caller that wires a real one.
	loginAuditor LoginAuditor
	// clock is overridden in tests; defaults to time.Now.
	clock func() time.Time
}

// NewService constructs a Service backed by users and the durable resets store. The resets
// argument is the production reset-token persistence (internal/store/postgres.ResetTokenRepository);
// tests pass the in-memory MemoryResetTokenStore.
func NewService(users UserStore, resets ResetTokenStore, logger *slog.Logger) *Service {
	return &Service{
		users:  users,
		resets: resets,
		logger: logger,
		clock:  time.Now,
	}
}

// SetLoginAuditor wires the login-audit write path. Until called, Authenticate succeeds without
// writing an audit entry — the same "unwired dependency is a no-op, not a failure" shape as the rest
// of this Service's optional collaborators.
func (s *Service) SetLoginAuditor(a LoginAuditor) {
	s.loginAuditor = a
}

// SignUp creates a new account for email/password.
//
// display_name is derived from the email (see deriveDisplayName) because the Sign Up screen collects
// no name field — a documented gap, reported upstream.
func (s *Service) SignUp(ctx context.Context, email, password string) (domain.User, error) {
	email = normalizeEmail(email)
	if err := validateEmail(email); err != nil {
		return domain.User{}, err
	}
	if err := validatePassword(password); err != nil {
		return domain.User{}, err
	}

	hash, err := HashPassword(password)
	if err != nil {
		return domain.User{}, fmt.Errorf("hashing password: %w", err)
	}

	user := domain.User{
		ID:          uuid.NewString(),
		Email:       email,
		DisplayName: deriveDisplayName(email),
		Tier:        domain.TierFree,
		CreatedAt:   s.clock(),
	}

	if err := s.users.Create(ctx, user, hash); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// Authenticate verifies email/password and returns the account on success.
//
// Returns ErrInvalidCredentials for BOTH "no such account" and "wrong password". Distinguishing the
// two would hand an attacker an account-enumeration oracle — see web/src/auth.ts, which relies on
// this same guarantee on the client side. A dummy hash comparison runs even on "no such account" so
// the response time does not itself leak which case occurred.
func (s *Service) Authenticate(ctx context.Context, email, password string) (domain.User, error) {
	email = normalizeEmail(email)

	user, hash, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			_, _ = VerifyPassword(dummyHash, password)
			return domain.User{}, ErrInvalidCredentials
		}
		return domain.User{}, err
	}

	ok, err := VerifyPassword(hash, password)
	if err != nil || !ok {
		return domain.User{}, ErrInvalidCredentials
	}

	// Written in the SAME code path as the successful login, per IMPLEMENTATION.md §5 rule 7 — never
	// reconciled after the fact. A write failure here is logged but does not fail the login itself:
	// an audit-log outage becoming a login outage would trade a monitoring problem for an
	// availability one, and nothing in FEAT-042 requires login to be blocked on it.
	if s.loginAuditor != nil {
		if err := s.loginAuditor.WriteLogin(ctx, user.ID); err != nil {
			s.logger.Error("writing login audit entry", slog.String("error", err.Error()), slog.String("user_id", user.ID))
		}
	}

	return user, nil
}

// RequestPasswordReset issues a reset token for email, if an account exists for it.
//
// Always returns nil for a "no such account" outcome — the caller sends one generic response
// regardless, so this method must never let the handler distinguish the two cases (the same
// no-enumeration rule as Authenticate). The token is logged, not emailed or returned to the
// caller: see reset_store.go's doc comment for the missing email-delivery integration this stands
// in for (the Transactional Email Provider is a vendor decision, per architecture.md). Only the
// token's one-way hash is persisted (schema.md's PasswordResetToken.token_hash); the raw token
// exists only in the delivered link and this log line.
func (s *Service) RequestPasswordReset(ctx context.Context, email string) error {
	email = normalizeEmail(email)

	user, _, err := s.users.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil
		}
		return err
	}

	token := randomToken()
	if err := s.resets.Issue(ctx, user.ID, hashToken(token), s.clock().Add(resetTokenTTL)); err != nil {
		return fmt.Errorf("issuing reset token: %w", err)
	}
	s.logger.Info("password reset requested",
		slog.String("user_id", user.ID),
		slog.String("reset_token", token),
	)
	return nil
}

// ConfirmPasswordReset consumes a reset token and sets a new password for the account it was issued
// to. Returns ErrInvalidResetToken for a missing, already-used, or expired token.
func (s *Service) ConfirmPasswordReset(ctx context.Context, token, newPassword string) error {
	userID, err := s.resets.Consume(ctx, hashToken(token))
	if err != nil {
		if errors.Is(err, ErrResetTokenNotFound) {
			return ErrInvalidResetToken
		}
		return fmt.Errorf("consuming reset token: %w", err)
	}
	if err := validatePassword(newPassword); err != nil {
		return err
	}

	hash, err := HashPassword(newPassword)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	return s.users.UpdatePasswordHash(ctx, userID, hash)
}

// dummyHash is compared against on a "no such account" login attempt purely for its cost — the
// verification work must take roughly the same time whether or not the account exists.
var dummyHash = mustHashDummy()

func mustHashDummy() string {
	hash, err := HashPassword("groundwork-timing-safety-placeholder")
	if err != nil {
		// HashPassword only fails if crypto/rand is broken, which ResetTokenStore's randomToken
		// treats the same way — a broken entropy source is not something the process can run
		// through safely.
		panic("auth: failed to precompute dummy hash: " + err.Error())
	}
	return hash
}

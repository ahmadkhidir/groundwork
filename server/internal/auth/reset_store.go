package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// resetTokenTTL is how long a password-reset token remains valid after issuance.
const resetTokenTTL = 30 * time.Minute

// ErrResetTokenNotFound is returned by a ResetTokenStore when no valid, unconsumed, unexpired token
// matches the supplied hash. Service maps it to its own ErrInvalidResetToken.
var ErrResetTokenNotFound = errors.New("reset token not found")

// ResetTokenStore is the persistence seam Service depends on for password-reset tokens, satisfied
// by internal/store/postgres.ResetTokenRepository in production. Defined here, consumer-side, so
// this package's tests can use an in-memory fake without importing the Postgres driver.
//
// Tokens are stored as one-way hashes per schema.md's PasswordResetToken.token_hash — the raw token
// exists only in the delivered link, never in the database. Issue receives the hash of the token the
// service generated and delivered; Consume receives the hash of the token the user submitted.
type ResetTokenStore interface {
	// Issue persists a token hash for userID, expiring at expiresAt.
	Issue(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	// Consume validates tokenHash, marks it consumed (single-use regardless of validity), and — on
	// success — invalidates the user's other outstanding tokens (screens/19 step 5). Returns the
	// user ID the token was issued to, or ErrResetTokenNotFound.
	Consume(ctx context.Context, tokenHash string) (string, error)
	// InvalidateForUser destroys every outstanding reset token for userID. Used by FEAT-052's
	// change-password flow so a previously-requested reset link cannot override the new password,
	// and by account deletion (the tokens die with the account anyway, but explicitly first).
	InvalidateForUser(ctx context.Context, userID string) error
}

// MemoryResetTokenStore is the in-memory ResetTokenStore used by Service tests (and by
// internal/api's handler tests). It mirrors the durable repository's contract — including
// single-use-on-any-attempt and invalidate-others-on-success — so the seam's behavior is pinned
// without a database. It is deliberately NOT wired in cmd/api/main.go: it does not survive a
// restart or a multi-instance deployment, which is exactly what the durable store exists to fix.
type MemoryResetTokenStore struct {
	mu      sync.Mutex
	entries map[string]resetEntry
}

type resetEntry struct {
	userID    string
	expiresAt time.Time
	consumed  bool
}

// NewMemoryResetTokenStore returns an empty store.
func NewMemoryResetTokenStore() *MemoryResetTokenStore {
	return &MemoryResetTokenStore{entries: make(map[string]resetEntry)}
}

// Issue records tokenHash for userID, expiring at expiresAt.
func (s *MemoryResetTokenStore) Issue(_ context.Context, userID, tokenHash string, expiresAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[tokenHash] = resetEntry{userID: userID, expiresAt: expiresAt}
	return nil
}

// Consume validates and invalidates tokenHash in one step. A token is single-use regardless of
// whether it is valid, so a leaked/observed token cannot be replayed after a legitimate use.
func (s *MemoryResetTokenStore) Consume(_ context.Context, tokenHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, found := s.entries[tokenHash]
	delete(s.entries, tokenHash)
	if !found || entry.consumed || time.Now().After(entry.expiresAt) {
		return "", ErrResetTokenNotFound
	}
	// On success, invalidate the user's other outstanding tokens (screens/19 step 5), matching the
	// durable repository's behavior.
	for hash, other := range s.entries {
		if other.userID == entry.userID {
			other.consumed = true
			s.entries[hash] = other
		}
	}
	return entry.userID, nil
}

// InvalidateForUser destroys every outstanding token for userID.
func (s *MemoryResetTokenStore) InvalidateForUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, entry := range s.entries {
		if entry.userID == userID {
			delete(s.entries, hash)
		}
	}
	return nil
}

// randomToken returns a cryptographically random, URL-safe token.
func randomToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failing indicates a broken host entropy source. There is no safe fallback for
		// a security token, so this is one of the rare cases where a panic is the correct response.
		panic("auth: failed to read random bytes: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// hashToken returns the one-way hash of a reset token, per schema.md's
// PasswordResetToken.token_hash. SHA-256 is the right primitive here (not a password KDF) because
// the token is a 256-bit cryptographic random value — there is no low-entropy secret to slow-hash.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

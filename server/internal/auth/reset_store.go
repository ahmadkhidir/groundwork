package auth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// resetTokenTTL is how long a password-reset token remains valid after issuance.
const resetTokenTTL = 30 * time.Minute

type resetEntry struct {
	userID    string
	expiresAt time.Time
}

// ResetTokenStore is an in-memory, process-local store of outstanding password-reset tokens.
//
// INTERIM IMPLEMENTATION, reported upstream: schema.md defines no PasswordResetToken entity and
// architecture.md wires no email-delivery integration, so there is (a) nowhere durable to persist a
// token yet and (b) no way to actually deliver one to a user's inbox. Tokens therefore live only in
// this process's memory — they do not survive a restart or a multi-instance deployment, and the
// "delivery" mechanism is a structured log line an operator can relay manually. This is enough to
// exercise the request/confirm flow end to end; it is not the production design.
type ResetTokenStore struct {
	mu      sync.Mutex
	entries map[string]resetEntry
}

// NewResetTokenStore returns an empty store.
func NewResetTokenStore() *ResetTokenStore {
	return &ResetTokenStore{entries: make(map[string]resetEntry)}
}

// Issue generates a new cryptographically random token for userID and records its expiry.
func (s *ResetTokenStore) Issue(userID string) string {
	token := randomToken()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[token] = resetEntry{userID: userID, expiresAt: time.Now().Add(resetTokenTTL)}
	return token
}

// Consume validates and invalidates token in one step — a token is single-use regardless of whether
// it is valid, so a leaked/observed token cannot be replayed after a legitimate use.
func (s *ResetTokenStore) Consume(token string) (userID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, found := s.entries[token]
	delete(s.entries, token)
	if !found || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.userID, true
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

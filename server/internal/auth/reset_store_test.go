package auth

import (
	"testing"
	"time"
)

func TestResetTokenStoreIssueAndConsume(t *testing.T) {
	store := NewResetTokenStore()

	token := store.Issue("user_1")
	if token == "" {
		t.Fatal("Issue() returned an empty token")
	}

	userID, ok := store.Consume(token)
	if !ok {
		t.Fatal("Consume() ok = false, want true for a freshly issued token")
	}
	if userID != "user_1" {
		t.Errorf("Consume() userID = %q, want user_1", userID)
	}
}

func TestResetTokenStoreTokenIsSingleUse(t *testing.T) {
	store := NewResetTokenStore()
	token := store.Issue("user_1")

	if _, ok := store.Consume(token); !ok {
		t.Fatal("first Consume() ok = false, want true")
	}

	// A leaked or observed token must not be replayable after a legitimate use.
	if _, ok := store.Consume(token); ok {
		t.Error("second Consume() ok = true, want false — a token must not be reusable")
	}
}

func TestResetTokenStoreRejectsUnknownToken(t *testing.T) {
	store := NewResetTokenStore()

	if _, ok := store.Consume("never-issued"); ok {
		t.Error("Consume() ok = true, want false for a token that was never issued")
	}
}

func TestResetTokenStoreRejectsExpiredToken(t *testing.T) {
	store := NewResetTokenStore()
	token := store.Issue("user_1")

	// Directly back-date the entry rather than sleeping resetTokenTTL in a test.
	store.mu.Lock()
	entry := store.entries[token]
	entry.expiresAt = time.Now().Add(-time.Minute)
	store.entries[token] = entry
	store.mu.Unlock()

	if _, ok := store.Consume(token); ok {
		t.Error("Consume() ok = true, want false for an expired token")
	}
}

func TestResetTokenStoreIssuesUniqueTokens(t *testing.T) {
	store := NewResetTokenStore()

	first := store.Issue("user_1")
	second := store.Issue("user_2")

	if first == second {
		t.Error("Issue() produced the same token for two different users")
	}
}

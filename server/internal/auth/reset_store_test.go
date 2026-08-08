package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The in-memory store's contract must mirror the durable repository's (single-use on any attempt,
// invalidate-others on success), so the seam's behavior is pinned without a database.

func TestMemoryResetTokenStoreIssueAndConsume(t *testing.T) {
	store := NewMemoryResetTokenStore()
	ctx := context.Background()

	if err := store.Issue(ctx, "user-1", "the-token-hash", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	userID, err := store.Consume(ctx, "the-token-hash")
	if err != nil {
		t.Fatalf("Consume() error = %v", err)
	}
	if userID != "user-1" {
		t.Errorf("userID = %q, want user-1", userID)
	}
}

func TestMemoryResetTokenStoreTokenIsSingleUse(t *testing.T) {
	store := NewMemoryResetTokenStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "user-1", "h", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if _, err := store.Consume(ctx, "h"); err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}
	if _, err := store.Consume(ctx, "h"); !errors.Is(err, ErrResetTokenNotFound) {
		t.Errorf("second Consume() error = %v, want ErrResetTokenNotFound", err)
	}
}

func TestMemoryResetTokenStoreRejectsUnknownToken(t *testing.T) {
	store := NewMemoryResetTokenStore()
	if _, err := store.Consume(context.Background(), "never-issued"); !errors.Is(err, ErrResetTokenNotFound) {
		t.Errorf("Consume() error = %v, want ErrResetTokenNotFound", err)
	}
}

func TestMemoryResetTokenStoreRejectsExpiredToken(t *testing.T) {
	store := NewMemoryResetTokenStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "user-1", "h", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if _, err := store.Consume(ctx, "h"); !errors.Is(err, ErrResetTokenNotFound) {
		t.Errorf("Consume() error = %v, want ErrResetTokenNotFound", err)
	}
}

func TestMemoryResetTokenStoreConsumeInvalidatesOtherTokensForUser(t *testing.T) {
	// screens/19 step 5: consuming one token invalidates the user's other outstanding tokens.
	store := NewMemoryResetTokenStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "user-1", "hash-a", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue(a) error = %v", err)
	}
	if err := store.Issue(ctx, "user-1", "hash-b", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue(b) error = %v", err)
	}

	if _, err := store.Consume(ctx, "hash-a"); err != nil {
		t.Fatalf("Consume(a) error = %v", err)
	}
	if _, err := store.Consume(ctx, "hash-b"); !errors.Is(err, ErrResetTokenNotFound) {
		t.Errorf("Consume(b) after a succeeded: error = %v, want ErrResetTokenNotFound", err)
	}
}

func TestMemoryResetTokenStoreConsumeLeavesOtherUsersTokensAlone(t *testing.T) {
	store := NewMemoryResetTokenStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "user-1", "hash-1", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue(user-1) error = %v", err)
	}
	if err := store.Issue(ctx, "user-2", "hash-2", time.Now().Add(resetTokenTTL)); err != nil {
		t.Fatalf("Issue(user-2) error = %v", err)
	}

	if _, err := store.Consume(ctx, "hash-1"); err != nil {
		t.Fatalf("Consume(user-1) error = %v", err)
	}
	if _, err := store.Consume(ctx, "hash-2"); err != nil {
		t.Errorf("Consume(user-2) after user-1 consumed: error = %v, want success", err)
	}
}

func TestHashTokenIsDeterministicAndOneWay(t *testing.T) {
	token := "some-raw-token"
	hash := hashToken(token)

	if hash == token {
		t.Error("hash must differ from the raw token")
	}
	if hashToken(token) != hash {
		t.Error("hashToken must be deterministic")
	}
	if len(hash) != 64 {
		t.Errorf("SHA-256 hex length = %d, want 64", len(hash))
	}
	if strings.Contains(hash, token) {
		t.Error("the raw token is recoverable from its hash")
	}
}

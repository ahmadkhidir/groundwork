package auth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeUserStore is an in-memory UserStore, keyed by email, for exercising Service without a real
// database — Service depends on the UserStore interface specifically so this is possible.
type fakeUserStore struct {
	byEmail map[string]domain.User
	byID    map[string]domain.User
	hashes  map[string]string // keyed by user id
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{
		byEmail: make(map[string]domain.User),
		byID:    make(map[string]domain.User),
		hashes:  make(map[string]string),
	}
}

func (f *fakeUserStore) Create(ctx context.Context, user domain.User, passwordHash string) error {
	if _, exists := f.byEmail[user.Email]; exists {
		return ErrEmailTaken
	}
	f.byEmail[user.Email] = user
	f.byID[user.ID] = user
	f.hashes[user.ID] = passwordHash
	return nil
}

func (f *fakeUserStore) FindByEmail(ctx context.Context, email string) (domain.User, string, error) {
	user, ok := f.byEmail[email]
	if !ok {
		return domain.User{}, "", ErrUserNotFound
	}
	return user, f.hashes[user.ID], nil
}

func (f *fakeUserStore) FindByID(ctx context.Context, id string) (domain.User, string, error) {
	user, ok := f.byID[id]
	if !ok {
		return domain.User{}, "", ErrUserNotFound
	}
	return user, f.hashes[id], nil
}

func (f *fakeUserStore) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	if _, ok := f.byID[userID]; !ok {
		return ErrUserNotFound
	}
	f.hashes[userID] = passwordHash
	return nil
}

func newTestService() *Service {
	return newTestServiceWithWriter(io.Discard)
}

// newTestServiceWithWriter wires the service's logger to w, so a test can capture the logged reset
// token — the only delivery channel until the Transactional Email Provider lands (the log line is
// what an operator relays manually).
func newTestServiceWithWriter(w io.Writer) *Service {
	return NewService(newFakeUserStore(), NewMemoryResetTokenStore(), slog.New(slog.NewTextHandler(w, nil)))
}

// captureResetToken parses the `reset_token=<value>` field out of a slog text-format log buffer.
func captureResetToken(t *testing.T, logOutput []byte) string {
	t.Helper()
	marker := "reset_token="
	idx := strings.Index(string(logOutput), marker)
	if idx < 0 {
		t.Fatalf("no reset_token in service log: %q", string(logOutput))
	}
	rest := string(logOutput)[idx+len(marker):]
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		rest = rest[:end]
	}
	if rest == "" {
		t.Fatal("empty reset_token in service log")
	}
	return rest
}

// fakeResetDeliverer records delivered reset links, or returns a configured error.
type fakeResetDeliverer struct {
	calls []struct{ to, token string }
	err   error
}

func (f *fakeResetDeliverer) DeliverPasswordReset(_ context.Context, to, token string) error {
	f.calls = append(f.calls, struct{ to, token string }{to, token})
	return f.err
}

func TestSignUpCreatesAnAccount(t *testing.T) {
	svc := newTestService()

	user, err := svc.SignUp(context.Background(), "Chidinma@Example.com", "a-real-password")
	if err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	// Acceptance criterion: user can create an account with email and password.
	if user.Email != "chidinma@example.com" {
		t.Errorf("Email = %q, want normalized lowercase chidinma@example.com", user.Email)
	}
	if user.ID == "" {
		t.Error("ID is empty")
	}
	if user.Tier != domain.TierFree {
		t.Errorf("Tier = %q, want %q for a new account", user.Tier, domain.TierFree)
	}
	if user.DisplayName == "" {
		t.Error("DisplayName is empty — schema.md requires it")
	}
}

func TestSignUpRejectsDuplicateEmail(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()

	if _, err := svc.SignUp(ctx, "tunde@example.com", "first-password"); err != nil {
		t.Fatalf("first SignUp() error = %v", err)
	}

	_, err := svc.SignUp(ctx, "tunde@example.com", "second-password")
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("second SignUp() error = %v, want ErrEmailTaken", err)
	}
}

func TestSignUpRejectsInvalidEmail(t *testing.T) {
	svc := newTestService()

	_, err := svc.SignUp(context.Background(), "not-an-email", "a-password")
	if !errors.Is(err, ErrInvalidEmail) {
		t.Errorf("SignUp() error = %v, want ErrInvalidEmail", err)
	}
}

func TestSignUpRejectsEmptyPassword(t *testing.T) {
	svc := newTestService()

	_, err := svc.SignUp(context.Background(), "ngozi@example.com", "")
	if !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("SignUp() error = %v, want ErrPasswordRequired", err)
	}
}

func TestAuthenticateSucceedsWithCorrectCredentials(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	user, err := svc.Authenticate(ctx, "chidinma@example.com", "correct-password")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	if user.Email != "chidinma@example.com" {
		t.Errorf("Email = %q, want chidinma@example.com", user.Email)
	}
}

// fakeLoginAuditor is an in-memory LoginAuditor for verifying Authenticate's audit write without a
// real database, matching the fakeUserStore pattern above.
type fakeLoginAuditor struct {
	loggedUserIDs []string
	failNext      bool
}

func (f *fakeLoginAuditor) WriteLogin(ctx context.Context, userID string) error {
	if f.failNext {
		return errors.New("simulated audit write failure")
	}
	f.loggedUserIDs = append(f.loggedUserIDs, userID)
	return nil
}

func TestAuthenticateWritesLoginAuditEntry(t *testing.T) {
	svc := newTestService()
	auditor := &fakeLoginAuditor{}
	svc.SetLoginAuditor(auditor)
	ctx := context.Background()

	user, err := svc.SignUp(ctx, "chidinma@example.com", "correct-password")
	if err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if _, err := svc.Authenticate(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if len(auditor.loggedUserIDs) != 1 || auditor.loggedUserIDs[0] != user.ID {
		t.Errorf("loggedUserIDs = %v, want [%s]", auditor.loggedUserIDs, user.ID)
	}
}

func TestAuthenticateDoesNotWriteLoginAuditEntryOnFailure(t *testing.T) {
	svc := newTestService()
	auditor := &fakeLoginAuditor{}
	svc.SetLoginAuditor(auditor)
	ctx := context.Background()

	if _, err := svc.SignUp(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if _, err := svc.Authenticate(ctx, "chidinma@example.com", "wrong-password"); err == nil {
		t.Fatal("expected Authenticate to fail with the wrong password")
	}

	if len(auditor.loggedUserIDs) != 0 {
		t.Errorf("loggedUserIDs = %v, want none on a failed login", auditor.loggedUserIDs)
	}
}

func TestAuthenticateSucceedsEvenWhenAuditWriteFails(t *testing.T) {
	svc := newTestService()
	auditor := &fakeLoginAuditor{failNext: true}
	svc.SetLoginAuditor(auditor)
	ctx := context.Background()

	if _, err := svc.SignUp(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	// An audit-log outage must not become a login outage — see Authenticate's doc comment.
	if _, err := svc.Authenticate(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("Authenticate() error = %v, want login to succeed despite the audit write failing", err)
	}
}

func TestAuthenticateSucceedsCaseInsensitiveEmail(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "chidinma@example.com", "correct-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if _, err := svc.Authenticate(ctx, "Chidinma@Example.com", "correct-password"); err != nil {
		t.Errorf("Authenticate() error = %v, want nil for a differently-cased email", err)
	}
}

// No-account-enumeration is the load-bearing security property of this feature: a wrong password
// and a nonexistent account must produce the exact same error to the caller.
func TestAuthenticateDoesNotDistinguishWrongPasswordFromNoSuchAccount(t *testing.T) {
	svc := newTestService()
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "tunde@example.com", "correct-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	_, wrongPasswordErr := svc.Authenticate(ctx, "tunde@example.com", "wrong-password")
	_, noSuchAccountErr := svc.Authenticate(ctx, "nobody@example.com", "anything")

	if !errors.Is(wrongPasswordErr, ErrInvalidCredentials) {
		t.Errorf("wrong password error = %v, want ErrInvalidCredentials", wrongPasswordErr)
	}
	if !errors.Is(noSuchAccountErr, ErrInvalidCredentials) {
		t.Errorf("no such account error = %v, want ErrInvalidCredentials", noSuchAccountErr)
	}
	if !errors.Is(wrongPasswordErr, noSuchAccountErr) && wrongPasswordErr.Error() != noSuchAccountErr.Error() {
		t.Error("wrong-password and no-such-account errors are distinguishable — enumeration risk")
	}
}

func TestPasswordResetRequestThenConfirmChangesThePassword(t *testing.T) {
	var logBuf bytes.Buffer
	svc := newTestServiceWithWriter(&logBuf)
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "ngozi@example.com", "old-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}

	if err := svc.RequestPasswordReset(ctx, "ngozi@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset() error = %v", err)
	}

	// The service logs the token rather than returning it (see reset_store.go's doc comment), so the
	// test captures it the way an operator relaying the logged token would.
	token := captureResetToken(t, logBuf.Bytes())

	if err := svc.ConfirmPasswordReset(ctx, token, "new-password"); err != nil {
		t.Fatalf("ConfirmPasswordReset() error = %v", err)
	}

	if _, err := svc.Authenticate(ctx, "ngozi@example.com", "old-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Error("old password still authenticates after reset")
	}
	if _, err := svc.Authenticate(ctx, "ngozi@example.com", "new-password"); err != nil {
		t.Errorf("Authenticate() with new password error = %v, want nil", err)
	}
}

func TestPasswordResetStoresOnlyTheTokenHash(t *testing.T) {
	// schema.md's PasswordResetToken.token_hash: the raw token is never persisted — the store key is
	// its one-way hash, and the raw token cannot be recovered from the store.
	var logBuf bytes.Buffer
	svc := newTestServiceWithWriter(&logBuf)
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "ngozi@example.com", "old-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "ngozi@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset() error = %v", err)
	}
	token := captureResetToken(t, logBuf.Bytes())

	entries := svc.resets.(*MemoryResetTokenStore).entries
	if len(entries) != 1 {
		t.Fatalf("stored %d tokens, want 1", len(entries))
	}
	for stored := range entries {
		if stored == token {
			t.Error("the raw token is stored verbatim — only the hash may be persisted")
		}
		if stored != hashToken(token) {
			t.Errorf("stored key = %q, want hashToken(token) = %q", stored, hashToken(token))
		}
	}
	// The raw token must not consume anything — only its hash matches a stored entry.
	if _, err := svc.resets.Consume(ctx, token); !errors.Is(err, ErrResetTokenNotFound) {
		t.Errorf("raw token consumed the store: error = %v, want ErrResetTokenNotFound", err)
	}
}

func TestPasswordResetDeliversTheLinkViaTheDeliverer(t *testing.T) {
	var logBuf bytes.Buffer
	svc := newTestServiceWithWriter(&logBuf)
	deliverer := &fakeResetDeliverer{}
	svc.SetResetTokenDeliverer(deliverer)
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "ngozi@example.com", "old-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "ngozi@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset() error = %v", err)
	}
	token := captureResetToken(t, logBuf.Bytes())

	if len(deliverer.calls) != 1 {
		t.Fatalf("deliverer got %d calls, want 1", len(deliverer.calls))
	}
	if deliverer.calls[0].to != "ngozi@example.com" {
		t.Errorf("deliverer recipient = %q, want ngozi@example.com", deliverer.calls[0].to)
	}
	if deliverer.calls[0].token != token {
		t.Error("deliverer received a token different from the one issued")
	}
}

func TestPasswordResetDeliveryFailureDoesNotFailTheRequest(t *testing.T) {
	// architecture.md: delivery is fire-and-forget — the reset-request flow never depends on email.
	svc := newTestService()
	svc.SetResetTokenDeliverer(&fakeResetDeliverer{err: errors.New("smtp down")})
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "ngozi@example.com", "old-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "ngozi@example.com"); err != nil {
		t.Errorf("RequestPasswordReset() error = %v, want nil even when email delivery fails", err)
	}
}

func TestPasswordResetRequestIsSilentForUnknownEmail(t *testing.T) {
	svc := newTestService()

	// Must return nil, not an error a handler could turn into a different response — that would
	// itself be an enumeration oracle.
	if err := svc.RequestPasswordReset(context.Background(), "nobody@example.com"); err != nil {
		t.Errorf("RequestPasswordReset() error = %v, want nil for an unknown email", err)
	}
}

func TestConfirmPasswordResetRejectsUnknownToken(t *testing.T) {
	svc := newTestService()

	err := svc.ConfirmPasswordReset(context.Background(), "not-a-real-token", "new-password")
	if !errors.Is(err, ErrInvalidResetToken) {
		t.Errorf("ConfirmPasswordReset() error = %v, want ErrInvalidResetToken", err)
	}
}

func TestConfirmPasswordResetTokenIsSingleUse(t *testing.T) {
	var logBuf bytes.Buffer
	svc := newTestServiceWithWriter(&logBuf)
	ctx := context.Background()
	if _, err := svc.SignUp(ctx, "ngozi@example.com", "old-password"); err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if err := svc.RequestPasswordReset(ctx, "ngozi@example.com"); err != nil {
		t.Fatalf("RequestPasswordReset() error = %v", err)
	}
	token := captureResetToken(t, logBuf.Bytes())

	if err := svc.ConfirmPasswordReset(ctx, token, "new-password-1"); err != nil {
		t.Fatalf("first ConfirmPasswordReset() error = %v", err)
	}

	err := svc.ConfirmPasswordReset(ctx, token, "new-password-2")
	if !errors.Is(err, ErrInvalidResetToken) {
		t.Errorf("second ConfirmPasswordReset() error = %v, want ErrInvalidResetToken", err)
	}
}

// Sanity check that the clock seam works as intended, in case a future test needs to freeze time
// (e.g. to test reset-token expiry through the Service rather than the store directly).
func TestServiceClockSeam(t *testing.T) {
	svc := newTestService()
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.clock = func() time.Time { return fixed }

	user, err := svc.SignUp(context.Background(), "fixed-clock@example.com", "password")
	if err != nil {
		t.Fatalf("SignUp() error = %v", err)
	}
	if !user.CreatedAt.Equal(fixed) {
		t.Errorf("CreatedAt = %v, want %v", user.CreatedAt, fixed)
	}
}

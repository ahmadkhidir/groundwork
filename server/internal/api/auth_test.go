package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeUserStore mirrors internal/auth's test double — kept local to this package (rather than
// exported from internal/auth) since only its own tests and this package need it, and an exported
// test helper would be an unusual public surface for a store.
type fakeUserStore struct {
	byEmail map[string]domain.User
	byID    map[string]domain.User
	hashes  map[string]string
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
		return auth.ErrEmailTaken
	}
	f.byEmail[user.Email] = user
	f.byID[user.ID] = user
	f.hashes[user.ID] = passwordHash
	return nil
}

func (f *fakeUserStore) FindByEmail(ctx context.Context, email string) (domain.User, string, error) {
	user, ok := f.byEmail[email]
	if !ok {
		return domain.User{}, "", auth.ErrUserNotFound
	}
	return user, f.hashes[user.ID], nil
}

func (f *fakeUserStore) FindByID(ctx context.Context, id string) (domain.User, string, error) {
	user, ok := f.byID[id]
	if !ok {
		return domain.User{}, "", auth.ErrUserNotFound
	}
	return user, f.hashes[id], nil
}

func (f *fakeUserStore) UpdatePasswordHash(ctx context.Context, userID, passwordHash string) error {
	if _, ok := f.byID[userID]; !ok {
		return auth.ErrUserNotFound
	}
	f.hashes[userID] = passwordHash
	return nil
}

// newTestServerWithAuth builds a Server wired to an in-memory auth.Service — every FEAT-001
// endpoint test in this file uses it instead of newTestServer (api_test.go), which deliberately
// passes a nil authService and so returns 404 for every /v1/auth/* route.
func newTestServerWithAuth(t *testing.T) *Server {
	t.Helper()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := auth.NewService(newFakeUserStore(), logger)
	return NewServer(cfg, logger, service)
}

func doJSON(t *testing.T, server *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response body: %v", err)
	}
	return body
}

func TestSignupCreatesAnAccount(t *testing.T) {
	server := newTestServerWithAuth(t)

	rec := doJSON(t, server, http.MethodPost, "/v1/auth/signup", map[string]string{
		"email":    "chidinma@example.com",
		"password": "a-real-password",
	})

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	body := decodeBody(t, rec)
	user, ok := body["user"].(map[string]any)
	if !ok {
		t.Fatalf("response has no user object: %v", body)
	}

	// The password (and any hash of it) must never appear in the response.
	if _, present := user["password"]; present {
		t.Error("response leaks a password field")
	}
	if _, present := user["password_hash"]; present {
		t.Error("response leaks a password_hash field")
	}
	if user["email"] != "chidinma@example.com" {
		t.Errorf("email = %v, want chidinma@example.com", user["email"])
	}
}

func TestSignupRejectsDuplicateEmail(t *testing.T) {
	server := newTestServerWithAuth(t)
	payload := map[string]string{"email": "tunde@example.com", "password": "first-password"}

	if rec := doJSON(t, server, http.MethodPost, "/v1/auth/signup", payload); rec.Code != http.StatusCreated {
		t.Fatalf("first signup status = %d, want %d", rec.Code, http.StatusCreated)
	}

	rec := doJSON(t, server, http.MethodPost, "/v1/auth/signup", payload)
	if rec.Code != http.StatusConflict {
		t.Errorf("second signup status = %d, want %d", rec.Code, http.StatusConflict)
	}
	if code := decodeBody(t, rec)["code"]; code != "email_taken" {
		t.Errorf("code = %v, want email_taken", code)
	}
}

func TestLoginSucceedsWithCorrectCredentials(t *testing.T) {
	server := newTestServerWithAuth(t)
	payload := map[string]string{"email": "ngozi@example.com", "password": "correct-password"}
	doJSON(t, server, http.MethodPost, "/v1/auth/signup", payload)

	rec := doJSON(t, server, http.MethodPost, "/v1/auth/login", payload)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

// The core security property under test: wrong password and no-such-account must produce an
// identical response, or the login endpoint becomes an account-enumeration oracle.
func TestLoginGivesIdenticalResponseForWrongPasswordAndNoSuchAccount(t *testing.T) {
	server := newTestServerWithAuth(t)
	doJSON(t, server, http.MethodPost, "/v1/auth/signup", map[string]string{
		"email": "tunde@example.com", "password": "correct-password",
	})

	wrongPassword := doJSON(t, server, http.MethodPost, "/v1/auth/login", map[string]string{
		"email": "tunde@example.com", "password": "wrong-password",
	})
	noSuchAccount := doJSON(t, server, http.MethodPost, "/v1/auth/login", map[string]string{
		"email": "nobody@example.com", "password": "anything",
	})

	if wrongPassword.Code != http.StatusUnauthorized {
		t.Errorf("wrong-password status = %d, want %d", wrongPassword.Code, http.StatusUnauthorized)
	}
	if noSuchAccount.Code != http.StatusUnauthorized {
		t.Errorf("no-such-account status = %d, want %d", noSuchAccount.Code, http.StatusUnauthorized)
	}
	if wrongPassword.Body.String() != noSuchAccount.Body.String() {
		t.Errorf("responses differ: %q vs %q — enumeration risk", wrongPassword.Body.String(), noSuchAccount.Body.String())
	}
}

func TestPasswordResetRequestAlwaysRespondsGenerically(t *testing.T) {
	server := newTestServerWithAuth(t)
	doJSON(t, server, http.MethodPost, "/v1/auth/signup", map[string]string{
		"email": "ngozi@example.com", "password": "a-password",
	})

	known := doJSON(t, server, http.MethodPost, "/v1/auth/password-reset/request", map[string]string{
		"email": "ngozi@example.com",
	})
	unknown := doJSON(t, server, http.MethodPost, "/v1/auth/password-reset/request", map[string]string{
		"email": "nobody@example.com",
	})

	if known.Code != http.StatusAccepted || unknown.Code != http.StatusAccepted {
		t.Fatalf("statuses = %d, %d, want both %d", known.Code, unknown.Code, http.StatusAccepted)
	}
	if known.Body.String() != unknown.Body.String() {
		t.Error("responses differ between a known and unknown email — enumeration risk")
	}
}

func TestSignupRejectsMalformedJSON(t *testing.T) {
	server := newTestServerWithAuth(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", bytes.NewBufferString("{not json"))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

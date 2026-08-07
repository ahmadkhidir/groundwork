package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// fakeEngineHandle is a minimal engine.Handle the wake fake returns. Wake never queries through it —
// it acquires, releases, and reports the resulting status — so the Query/Exec bodies are empty.
type fakeEngineHandle struct {
	workspaceID string
}

func (h *fakeEngineHandle) WorkspaceID() string { return h.workspaceID }
func (h *fakeEngineHandle) Query(context.Context, string, ...any) (engine.Rows, error) {
	return nil, nil
}
func (h *fakeEngineHandle) Exec(context.Context, string, ...any) (int64, error) {
	return 0, nil
}
func (h *fakeEngineHandle) AcquiredAt() time.Time { return time.Now() }

// fakeWakeProvider is an in-memory EngineWakeProvider recording acquires and releases.
type fakeWakeProvider struct {
	released []string
	err      error
}

func (f *fakeWakeProvider) Acquire(_ context.Context, workspaceID string) (engine.Handle, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &fakeEngineHandle{workspaceID: workspaceID}, nil
}

func (f *fakeWakeProvider) Release(handle engine.Handle) error {
	f.released = append(f.released, handle.WorkspaceID())
	return nil
}

// newTestWakeRouter wires RegisterEngineWakeRoute onto a fresh authenticated chi router, mirroring
// how api.go mounts protected routes.
func newTestWakeRouter(workspaces WorkspaceStore, provider EngineWakeProvider) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterEngineWakeRoute(r, workspaces, newFakeMemberLookupStore(), newFakeUserTierLookup(), provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

func wakeTestFixtures(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeWakeProvider) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1", Name: "Q3 Numbers"}
	provider := &fakeWakeProvider{}
	return newTestWakeRouter(workspaces, provider), workspaces, provider
}

func TestWakeWorkspaceRequiresAuthentication(t *testing.T) {
	router, _, _ := wakeTestFixtures(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestWakeWorkspaceSucceeds is FEAT-012 AC1 at the API layer: focusing the Ask input fires this
// POST, and a successful wake reports the engine ready without error.
func TestWakeWorkspaceSucceeds(t *testing.T) {
	router, _, provider := wakeTestFixtures(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		EngineStatus string `json:"engine_status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.EngineStatus != string(engine.StatusReady) {
		t.Errorf("engine_status = %q, want %q (Acquire only returns once ready)", body.EngineStatus, engine.StatusReady)
	}
	// Acquire must be paired with Release: the wake does not hold a lease it is not using.
	if len(provider.released) != 1 || provider.released[0] != "ws-1" {
		t.Errorf("released = %v, want exactly [ws-1]", provider.released)
	}
}

// TestWakeWorkspaceUnavailableIsHonest: the engine could not be provisioned within the deadline —
// the response is a retryable 503 with the honest engine_unavailable code, never a generic 500 and
// never an error state that poisons later questions (run creation wakes the engine on its own path).
func TestWakeWorkspaceUnavailableIsHonest(t *testing.T) {
	router, _, provider := wakeTestFixtures(t)
	provider.err = engine.ErrEngineUnavailable

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["code"] != "engine_unavailable" {
		t.Errorf("code = %q, want engine_unavailable", body["code"])
	}
}

func TestWakeWorkspaceNonOwnerNotFound(t *testing.T) {
	router, _, _ := wakeTestFixtures(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestWakeWorkspaceUnusedDoesNotError is FEAT-012 AC2 (Flow 4 Alternate Path A): a wake fired on
// focus that is never followed by a question must not error or leave a bad state. The endpoint is
// idempotent, and a repeated wake (focus, blur, focus again) succeeds every time.
func TestWakeWorkspaceUnusedDoesNotError(t *testing.T) {
	router, _, _ := wakeTestFixtures(t)

	for i := 0; i < 3; i++ {
		req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil, "user-1", domain.TierFree)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("wake %d: status = %d, want %d, body = %s", i, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}

// TestWakeWorkspaceProviderErrorFailsHonestly covers a non-provisioning provider failure: 500 with
// the generic error, distinct from the honest engine_unavailable 503.
func TestWakeWorkspaceProviderErrorFailsHonestly(t *testing.T) {
	router, _, provider := wakeTestFixtures(t)
	provider.err = errors.New("provider exploded")

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/engine/wake", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

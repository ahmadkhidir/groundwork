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

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/engine"
)

// fakeEngineStatusProvider is an in-memory EngineStatusProvider.
//
// NOTE for assembly: Dependencies (api.go) has no EngineStatusProvider field yet — wiring
// RegisterEngineRoutes into the real Server is the orchestrator's job (see this wave's report).
// These tests build a standalone chi.Router with authsession.RequireAuth directly, exactly as
// RegisterEngineRoutes will be mounted in production, rather than going through NewServer.
type fakeEngineStatusProvider struct {
	byWorkspace map[string]engine.Status
	err         error
}

func newFakeEngineStatusProvider() *fakeEngineStatusProvider {
	return &fakeEngineStatusProvider{byWorkspace: make(map[string]engine.Status)}
}

func (f *fakeEngineStatusProvider) Status(ctx context.Context, workspaceID string) (engine.Status, error) {
	if f.err != nil {
		return "", f.err
	}
	status, ok := f.byWorkspace[workspaceID]
	if !ok {
		return engine.StatusSleeping, nil
	}
	return status, nil
}

// newTestEngineRouter builds a minimal, real authenticated router around RegisterEngineRoutes —
// mirroring exactly how api.go mounts every other protected route (RequireAuth, then the
// feature's Register*Routes), without requiring a Dependencies field this wave doesn't own.
func newTestEngineRouter(workspaces WorkspaceStore, provider EngineStatusProvider) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterEngineRoutes(r, workspaces, provider, slog.New(slog.NewTextHandler(io.Discard, nil)))
		})
	})
	return r
}

func TestGetWorkspaceRequiresAuthentication(t *testing.T) {
	router := newTestEngineRouter(newFakeWorkspaceStore(), newFakeEngineStatusProvider())

	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestGetWorkspaceMergesLiveEngineStatus is FEAT-011's core acceptance criterion at the API layer:
// the response reflects engine.Provider's LIVE status, not whatever was last persisted at creation.
func TestGetWorkspaceMergesLiveEngineStatus(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{
		ID:      "ws-1",
		Name:    "Q3 Numbers",
		OwnerID: "user-1",
		// Persisted value is stale — deliberately "sleeping" — to prove the live value wins.
		EngineStatus: domain.EngineSleeping,
	}

	provider := newFakeEngineStatusProvider()
	provider.byWorkspace["ws-1"] = engine.StatusReady

	router := newTestEngineRouter(workspaces, provider)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Workspace struct {
			Name         string `json:"name"`
			EngineStatus string `json:"engine_status"`
		} `json:"workspace"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Workspace.Name != "Q3 Numbers" {
		t.Errorf("Name = %q, want %q", body.Workspace.Name, "Q3 Numbers")
	}
	if body.Workspace.EngineStatus != string(engine.StatusReady) {
		t.Errorf("EngineStatus = %q, want %q (must reflect the LIVE Provider status, not the persisted one)", body.Workspace.EngineStatus, engine.StatusReady)
	}
}

// TestGetWorkspaceFallsBackToPersistedStatusOnProviderError verifies a transient failure to read
// live status degrades to the persisted value rather than failing a request that otherwise succeeded
// — the workspace record itself loaded fine.
func TestGetWorkspaceFallsBackToPersistedStatusOnProviderError(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{
		ID:           "ws-1",
		Name:         "Q3 Numbers",
		OwnerID:      "user-1",
		EngineStatus: domain.EngineWaking,
	}

	provider := newFakeEngineStatusProvider()
	provider.err = errors.New("boom")

	router := newTestEngineRouter(workspaces, provider)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Workspace struct {
			EngineStatus string `json:"engine_status"`
		} `json:"workspace"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Workspace.EngineStatus != string(domain.EngineWaking) {
		t.Errorf("EngineStatus = %q, want %q (persisted fallback)", body.Workspace.EngineStatus, domain.EngineWaking)
	}
}

// TestGetWorkspaceNotFoundForNonOwner exercises the same non-enumeration behavior every other
// workspace-scoped route shares via requireWorkspaceOwnership.
func TestGetWorkspaceNotFoundForNonOwner(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}

	router := newTestEngineRouter(workspaces, newFakeEngineStatusProvider())

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestGetWorkspaceNotFoundForMissingWorkspace confirms a genuinely nonexistent workspace also 404s.
func TestGetWorkspaceNotFoundForMissingWorkspace(t *testing.T) {
	router := newTestEngineRouter(newFakeWorkspaceStore(), newFakeEngineStatusProvider())

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/does-not-exist", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

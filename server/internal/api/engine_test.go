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
	return newTestEngineRouterWithDatasets(workspaces, provider, &fakeDatasetPresence{})
}

func newTestEngineRouterWithDatasets(workspaces WorkspaceStore, provider EngineStatusProvider, datasets WorkspaceDatasetPresence) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterEngineRoutes(r, workspaces, provider, datasets, slog.New(slog.NewTextHandler(io.Discard, nil)))
		})
	})
	return r
}

// fakeDatasetPresence is an in-memory WorkspaceDatasetPresence backing screen 04's has_datasets.
type fakeDatasetPresence struct {
	withDatasets map[string]bool
	err          error
}

func (f *fakeDatasetPresence) ExistsForWorkspace(ctx context.Context, workspaceID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.withDatasets[workspaceID], nil
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

// TestGetWorkspaceReportsDatasetPresence covers screens/04-workspace-home.md's has_datasets field,
// which drives whether the "Upload data" CTA is shown.
func TestGetWorkspaceReportsDatasetPresence(t *testing.T) {
	cases := []struct {
		name string
		has  bool
	}{
		{"no files ingested yet", false},
		{"at least one dataset", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspaces := newFakeWorkspaceStore()
			workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1", EngineStatus: domain.EngineSleeping}
			datasets := &fakeDatasetPresence{withDatasets: map[string]bool{"ws-1": tc.has}}
			router := newTestEngineRouterWithDatasets(workspaces, newFakeEngineStatusProvider(), datasets)

			req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Workspace struct {
					HasDatasets bool `json:"has_datasets"`
				} `json:"workspace"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decoding body: %v", err)
			}
			if body.Workspace.HasDatasets != tc.has {
				t.Errorf("has_datasets = %v, want %v", body.Workspace.HasDatasets, tc.has)
			}
		})
	}
}

// TestGetWorkspaceFailsWhenDatasetPresenceIsUnknown: unlike live engine status, has_datasets has no
// honest fallback. Defaulting to false would tell a user who already uploaded files that they have
// none, and offer them an "upload your first file" prompt.
func TestGetWorkspaceFailsWhenDatasetPresenceIsUnknown(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	datasets := &fakeDatasetPresence{err: errors.New("database unavailable")}
	router := newTestEngineRouterWithDatasets(workspaces, newFakeEngineStatusProvider(), datasets)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

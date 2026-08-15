package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/config"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

const testAuthSecret = "test-only-secret-for-handler-tests"

// fakeWorkspaceStore is an in-memory WorkspaceStore.
type fakeWorkspaceStore struct {
	byID map[string]domain.Workspace
}

func newFakeWorkspaceStore() *fakeWorkspaceStore {
	return &fakeWorkspaceStore{byID: make(map[string]domain.Workspace)}
}

func (f *fakeWorkspaceStore) Create(ctx context.Context, ws domain.Workspace) error {
	f.byID[ws.ID] = ws
	return nil
}

func (f *fakeWorkspaceStore) ListByOwner(ctx context.Context, ownerID string) ([]domain.Workspace, error) {
	// Deterministic order (by id), mirroring the real repository's ListByOwner contract — the
	// postgres store orders deterministically (ORDER BY updated_at DESC), and a map iteration here
	// made every consumer of this fake (account deletion's cascade, billing access) order-dependent
	// and flaky under test.
	ids := make([]string, 0, len(f.byID))
	for id, ws := range f.byID {
		if ws.OwnerID == ownerID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := make([]domain.Workspace, 0, len(ids))
	for _, id := range ids {
		out = append(out, f.byID[id])
	}
	return out, nil
}

func (f *fakeWorkspaceStore) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	count := 0
	for _, ws := range f.byID {
		if ws.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (f *fakeWorkspaceStore) GetByID(ctx context.Context, id string) (domain.Workspace, error) {
	ws, ok := f.byID[id]
	if !ok {
		return domain.Workspace{}, postgres.ErrWorkspaceNotFound
	}
	return ws, nil
}

// fakeUserTierLookup is an in-memory UserTierLookup.
type fakeUserTierLookup struct {
	tiers map[string]domain.SubscriptionTier
}

func newFakeUserTierLookup() *fakeUserTierLookup {
	return &fakeUserTierLookup{tiers: make(map[string]domain.SubscriptionTier)}
}

func (f *fakeUserTierLookup) FindByID(ctx context.Context, id string) (domain.User, string, error) {
	tier, ok := f.tiers[id]
	if !ok {
		tier = domain.TierFree
	}
	return domain.User{ID: id, Tier: tier}, "", nil
}

// testServerDeps bundles the fakes a workspace/upload/audit test wires into Dependencies, so each
// test file can reach into the same in-memory stores it asserts against.
type testServerDeps struct {
	workspaces *fakeWorkspaceStore
	users      *fakeUserTierLookup
	datasets   *fakeDatasetStore
	objects    *fakeObjectStore
	auditLog   *fakeAuditStore
}

func newTestServerWithDeps(t *testing.T) (*Server, testServerDeps) {
	t.Helper()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: config.MaxUploadBytesDefault,
		Environment:    "development",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	deps := testServerDeps{
		workspaces: newFakeWorkspaceStore(),
		users:      newFakeUserTierLookup(),
		datasets:   newFakeDatasetStore(),
		objects:    newFakeObjectStore(),
		auditLog:   newFakeAuditStore(),
	}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret: testAuthSecret,
		Workspaces: deps.workspaces,
		Users:      deps.users,
		Datasets:   deps.datasets,
		Objects:    deps.objects,
		AuditLog:   deps.auditLog,
	})
	return server, deps
}

// authedRequest builds a request carrying a valid Auth.js session cookie for userID/tier, using
// authsession.Encode — the same scheme web/src/auth.ts's real cookie uses, verified in
// internal/authsession/session_test.go against an actual Auth.js-issued token.
func authedRequest(t *testing.T, method, path string, body any, userID string, tier domain.SubscriptionTier) *http.Request {
	t.Helper()

	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshaling body: %v", err)
		}
		reader = bytes.NewReader(buf)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	attachSessionCookie(t, req, userID, tier)
	return req
}

// attachSessionCookie adds a valid Auth.js session cookie to an already-constructed request — used
// by upload_test.go, whose requests are built as multipart bodies rather than through authedRequest.
func attachSessionCookie(t *testing.T, req *http.Request, userID string, tier domain.SubscriptionTier) string {
	t.Helper()

	token, err := authsession.Encode(authsession.Claims{UserID: userID, Tier: string(tier)}, testAuthSecret)
	if err != nil {
		t.Fatalf("encoding session token: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: authsession.CookieName, Value: token})
	return token
}

// newTestServerWithTinyUploadLimit builds a server whose MaxUploadBytes is small enough to test the
// oversized-file rejection path without allocating a 500MB+ byte slice in the test process.
func newTestServerWithTinyUploadLimit(t *testing.T) (*Server, testServerDeps) {
	t.Helper()

	cfg := config.Config{
		Port:           "8080",
		DatabaseURL:    "postgres://localhost/test",
		AllowedOrigin:  "http://localhost:3000",
		MaxUploadBytes: 10, // 10 bytes
		Environment:    "development",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	deps := testServerDeps{
		workspaces: newFakeWorkspaceStore(),
		users:      newFakeUserTierLookup(),
		datasets:   newFakeDatasetStore(),
		objects:    newFakeObjectStore(),
		auditLog:   newFakeAuditStore(),
	}

	server := NewServer(cfg, logger, nil, Dependencies{
		AuthSecret: testAuthSecret,
		Workspaces: deps.workspaces,
		Users:      deps.users,
		Datasets:   deps.datasets,
		Objects:    deps.objects,
		AuditLog:   deps.auditLog,
	})
	return server, deps
}

func TestCreateWorkspaceRequiresAuthentication(t *testing.T) {
	server, _ := newTestServerWithDeps(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces", bytes.NewReader([]byte(`{"name":"Q3 Numbers"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestCreateWorkspaceSucceeds(t *testing.T) {
	server, deps := newTestServerWithDeps(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "Q3 Numbers"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	var body struct {
		Workspace struct {
			Name         string `json:"name"`
			OwnerID      string `json:"owner_id"`
			EngineStatus string `json:"engine_status"`
		} `json:"workspace"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Workspace.Name != "Q3 Numbers" {
		t.Errorf("Name = %q, want %q", body.Workspace.Name, "Q3 Numbers")
	}
	if body.Workspace.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want %q", body.Workspace.OwnerID, "user-1")
	}
	// FEAT-002 acceptance criterion: creating a workspace never provisions an engine.
	if body.Workspace.EngineStatus != string(domain.EngineSleeping) {
		t.Errorf("EngineStatus = %q, want %q (creation must never provision an engine)", body.Workspace.EngineStatus, domain.EngineSleeping)
	}
	if len(deps.workspaces.byID) != 1 {
		t.Errorf("stored workspace count = %d, want 1", len(deps.workspaces.byID))
	}
}

func TestCreateWorkspaceRejectsEmptyName(t *testing.T) {
	server, _ := newTestServerWithDeps(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "   "}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestCreateWorkspaceEnforcesFreeTierLimit verifies monetization.md's FEAT-002 gate: Free = 1
// workspace. The gate is checked server-side against the AUTHORITATIVE tier (fakeUserTierLookup),
// never the session cookie's tier claim — see UserTierLookup's doc comment.
func TestCreateWorkspaceEnforcesFreeTierLimit(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["existing"] = domain.Workspace{ID: "existing", OwnerID: "user-1", Name: "Already here"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "One too many"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "workspace_limit_reached" {
		t.Errorf("code = %q, want workspace_limit_reached", body["code"])
	}
}

func TestCreateWorkspaceAllowsProTierUpToFive(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.users.tiers["user-1"] = domain.TierPro
	for i := 0; i < 4; i++ {
		id := "ws-" + string(rune('a'+i))
		deps.workspaces.byID[id] = domain.Workspace{ID: id, OwnerID: "user-1"}
	}

	// The 5th workspace should succeed (4 existing + 1 new = 5, the Pro ceiling).
	req := authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "Fifth"}, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("5th workspace: status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}

	// The 6th should be rejected.
	req = authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "Sixth"}, "user-1", domain.TierPro)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("6th workspace: status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestCreateWorkspaceTeamTierIsUnlimited(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.users.tiers["user-1"] = domain.TierTeam
	for i := 0; i < 20; i++ {
		id := "ws-" + string(rune('a'+i))
		deps.workspaces.byID[id] = domain.Workspace{ID: id, OwnerID: "user-1"}
	}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces", map[string]string{"name": "21st"}, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
}

func TestListWorkspacesReturnsOnlyTheOwners(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["mine"] = domain.Workspace{ID: "mine", OwnerID: "user-1", Name: "Mine", UpdatedAt: time.Now()}
	deps.workspaces.byID["theirs"] = domain.Workspace{ID: "theirs", OwnerID: "user-2", Name: "Theirs", UpdatedAt: time.Now()}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Workspaces []struct {
			Name string `json:"name"`
		} `json:"workspaces"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Workspaces) != 1 || body.Workspaces[0].Name != "Mine" {
		t.Errorf("Workspaces = %+v, want exactly [Mine]", body.Workspaces)
	}
}

// TestListWorkspacesEmptyAccount verifies the empty case screens/03-workspace-list.md distinguishes
// from an error: a brand-new account gets an empty list, 200 OK, not a 404 or error.
func TestListWorkspacesEmptyAccount(t *testing.T) {
	server, _ := newTestServerWithDeps(t)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces", nil, "brand-new-user", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Workspaces []any `json:"workspaces"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Workspaces) != 0 {
		t.Errorf("Workspaces = %v, want empty", body.Workspaces)
	}
}

// TestNonOwnerCannotAccessAnotherUsersWorkspace exercises requireWorkspaceOwnership, shared by
// upload.go and audit.go: a non-owner gets 404, indistinguishable from the workspace not existing.
func TestNonOwnerCannotAccessAnotherUsersWorkspace(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/audit-log", nil, "someone-else", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

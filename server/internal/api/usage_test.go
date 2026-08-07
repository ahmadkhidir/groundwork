package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// fakeUsageStore is an in-memory UsageStore.
type fakeUsageStore struct {
	byWorkspace map[string][]domain.UsageRecord
	err         error
}

func newFakeUsageStore() *fakeUsageStore {
	return &fakeUsageStore{byWorkspace: make(map[string][]domain.UsageRecord)}
}

func (f *fakeUsageStore) ListByWorkspace(_ context.Context, workspaceID string, _ int) ([]domain.UsageRecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byWorkspace[workspaceID], nil
}

// newTestUsageRouter wires RegisterUsageRoutes onto a fresh authenticated chi router, passing the
// member lookup the read-only access check needs.
func newTestUsageRouter(workspaces WorkspaceStore, members MemberLookupStore, usage UsageStore) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterUsageRoutes(r, workspaces, members, usage, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

func usageTestFixtures(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeMemberLookupStore, *fakeUsageStore) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1", Name: "Q3 Numbers"}
	members := newFakeMemberLookupStore()
	usage := newFakeUsageStore()
	return newTestUsageRouter(workspaces, members, usage), workspaces, members, usage
}

func TestGetUsageRequiresAuthentication(t *testing.T) {
	router, _, _, _ := usageTestFixtures(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/workspaces/ws-1/usage", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestGetUsageOwnerServesRecordsAndTotals is FEAT-047 AC1 at the API layer: the owner sees
// engine-seconds, storage bytes, and model tokens for the workspace.
func TestGetUsageOwnerServesRecordsAndTotals(t *testing.T) {
	router, _, _, usage := usageTestFixtures(t)
	usage.byWorkspace["ws-1"] = []domain.UsageRecord{
		{ID: "u-2", WorkspaceID: "ws-1", PeriodStart: time.Now().Add(-24 * time.Hour), PeriodEnd: time.Now(),
			EngineSeconds: 42.5, StorageBytes: 2048, ModelTokens: 1200},
		{ID: "u-1", WorkspaceID: "ws-1", PeriodStart: time.Now().Add(-48 * time.Hour), PeriodEnd: time.Now().Add(-24 * time.Hour),
			EngineSeconds: 10, StorageBytes: 1024, ModelTokens: 300},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/usage", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Usage struct {
			Records []domain.UsageRecord `json:"records"`
			Totals  struct {
				EngineSeconds float64 `json:"engine_seconds"`
				StorageBytes  int64   `json:"storage_bytes"`
				ModelTokens   int64   `json:"model_tokens"`
			} `json:"totals"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Usage.Records) != 2 {
		t.Errorf("records = %d, want 2", len(body.Usage.Records))
	}
	// Totals reflect the CURRENT period — the most recent record, per ListByWorkspace's ordering.
	if body.Usage.Totals.EngineSeconds != 42.5 {
		t.Errorf("totals.engine_seconds = %v, want 42.5 (the latest record's values)", body.Usage.Totals.EngineSeconds)
	}
	if body.Usage.Totals.ModelTokens != 1200 {
		t.Errorf("totals.model_tokens = %d, want 1200", body.Usage.Totals.ModelTokens)
	}
}

// TestGetUsageReadOnlyMemberAllowed is screens/13's read-only treatment: a shared read-only member
// sees the Usage tab (the one settings surface they are granted), so the endpoint must authorize via
// membership, not ownership.
func TestGetUsageReadOnlyMemberAllowed(t *testing.T) {
	router, _, members, usage := usageTestFixtures(t)
	members.byUser["user-2"] = domain.WorkspaceMember{ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly}
	usage.byWorkspace["ws-1"] = []domain.UsageRecord{}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/usage", nil, "user-2", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (read-only members see usage), body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}

func TestGetUsageNonMemberNotFound(t *testing.T) {
	router, _, _, _ := usageTestFixtures(t)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/usage", nil, "stranger", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestGetUsageEmptyWorkspaceIsHonest: no usage records yet is a 200 with an empty list and zero
// totals — never an error, and never a fabricated number (usage metering writes lag usage; the
// screen's empty state is the honest "nothing recorded yet").
func TestGetUsageEmptyWorkspaceIsHonest(t *testing.T) {
	router, _, _, _ := usageTestFixtures(t)

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/usage", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body struct {
		Usage struct {
			Records []any `json:"records"`
			Totals  struct {
				EngineSeconds float64 `json:"engine_seconds"`
				StorageBytes  int64   `json:"storage_bytes"`
				ModelTokens   int64   `json:"model_tokens"`
			} `json:"totals"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Usage.Records) != 0 {
		t.Errorf("records = %v, want empty", body.Usage.Records)
	}
	if body.Usage.Totals.EngineSeconds != 0 || body.Usage.Totals.ModelTokens != 0 {
		t.Errorf("totals = %+v, want zeros when nothing is recorded", body.Usage.Totals)
	}
}

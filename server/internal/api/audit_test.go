package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeAuditStore is an in-memory AuditStore mirroring postgres.AuditRepository.List's contract
// closely enough to exercise the handler (scoping, filters, cursor) without a real database.
type fakeAuditStore struct {
	entries []domain.AuditLogEntry
}

func newFakeAuditStore() *fakeAuditStore {
	return &fakeAuditStore{}
}

func (f *fakeAuditStore) List(ctx context.Context, filter postgres.ListFilter) (postgres.Page, error) {
	var matched []domain.AuditLogEntry
	for _, e := range f.entries {
		inScope := e.WorkspaceID == filter.WorkspaceID ||
			(e.WorkspaceID == "" && e.UserID == filter.RequestingUserID)
		if !inScope {
			continue
		}
		if filter.ActionType != "" && string(e.ActionType) != filter.ActionType {
			continue
		}
		matched = append(matched, e)
	}
	// Most recent first, matching the real repository's ORDER BY.
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}
	return postgres.Page{Entries: matched}, nil
}

func TestAuditLogMergesWorkspaceAndAccountScopedEntries(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	deps.auditLog.entries = []domain.AuditLogEntry{
		{ID: "e1", WorkspaceID: "ws-1", UserID: "user-1", ActionType: domain.ActionQueryExecuted, Timestamp: time.Now()},
		{ID: "e2", WorkspaceID: "", UserID: "user-1", ActionType: domain.ActionLogin, Timestamp: time.Now()},
		// Another user's login must never appear, even though this workspace's owner is asking.
		{ID: "e3", WorkspaceID: "", UserID: "user-2", ActionType: domain.ActionLogin, Timestamp: time.Now()},
		// Another workspace's entry must never appear.
		{ID: "e4", WorkspaceID: "ws-2", UserID: "user-1", ActionType: domain.ActionQueryExecuted, Timestamp: time.Now()},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/audit-log", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Entries []struct {
			ID    string `json:"id"`
			Scope string `json:"scope"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	ids := make(map[string]string)
	for _, e := range body.Entries {
		ids[e.ID] = e.Scope
	}
	if len(ids) != 2 {
		t.Fatalf("Entries = %+v, want exactly e1 and e2", body.Entries)
	}
	if ids["e1"] != "workspace" {
		t.Errorf("e1 scope = %q, want workspace", ids["e1"])
	}
	if ids["e2"] != "account" {
		t.Errorf("e2 scope = %q, want account", ids["e2"])
	}
}

func TestAuditLogEmptyWorkspaceReturnsEmptyList(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/audit-log", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Entries []any `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Entries) != 0 {
		t.Errorf("Entries = %v, want empty for a brand-new workspace", body.Entries)
	}
}

func TestAuditLogFiltersByActionType(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	deps.auditLog.entries = []domain.AuditLogEntry{
		{ID: "e1", WorkspaceID: "ws-1", UserID: "user-1", ActionType: domain.ActionQueryExecuted, Timestamp: time.Now()},
		{ID: "e2", WorkspaceID: "ws-1", UserID: "user-1", ActionType: domain.ActionExport, Timestamp: time.Now()},
	}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/audit-log?action_type=export", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	var body struct {
		Entries []struct {
			ID string `json:"id"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Entries) != 1 || body.Entries[0].ID != "e2" {
		t.Errorf("Entries = %+v, want exactly [e2]", body.Entries)
	}
}

func TestAuditLogNonOwnerCannotAccess(t *testing.T) {
	server, deps := newTestServerWithDeps(t)
	deps.workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "owner-user"}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/audit-log", nil, "read-only-member", domain.TierFree)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	// screens/17-egress-audit-log.md: a non-owner is hidden entirely, not just read-only.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// wsAt builds a workspace owned by owner with a specific UpdatedAt, for the gate's recency tiebreak.
func wsAt(id, owner string, updatedAt time.Time) domain.Workspace {
	return domain.Workspace{ID: id, OwnerID: owner, UpdatedAt: updatedAt}
}

// TestOwnerMutationAllowedByTier pins the downgrade gate: under the tier's workspace ceiling every
// workspace is writable; over it only the MOST RECENTLY UPDATED workspace stays writable (the same
// workspace the list orders first); unlimited tiers are never gated; a missing tier seam degrades to
// allowed.
func TestOwnerMutationAllowedByTier(t *testing.T) {
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	older := now.Add(-24 * time.Hour)

	build := func(tier domain.SubscriptionTier, workspaces ...domain.Workspace) (*fakeWorkspaceStore, *fakeUserTierLookup) {
		stores := newFakeWorkspaceStore()
		for _, ws := range workspaces {
			stores.byID[ws.ID] = ws
		}
		users := newFakeUserTierLookup()
		users.tiers["user-1"] = tier
		return stores, users
	}

	call := func(stores *fakeWorkspaceStore, users *fakeUserTierLookup, ws domain.Workspace, userID string) (*httptest.ResponseRecorder, bool) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/whatever", nil)
		ok := ownerMutationAllowedByTier(rec, req, stores, users, ws, userID)
		return rec, ok
	}

	t.Run("free single workspace allowed", func(t *testing.T) {
		stores, users := build(domain.TierFree, wsAt("ws-1", "user-1", now))
		rec, ok := call(stores, users, stores.byID["ws-1"], "user-1")
		if !ok || rec.Code != http.StatusOK {
			t.Fatalf("ok=%v status=%d, want allowed", ok, rec.Code)
		}
	})

	t.Run("free over limit most recent writable", func(t *testing.T) {
		stores, users := build(domain.TierFree,
			wsAt("ws-old", "user-1", older), wsAt("ws-new", "user-1", now))
		rec, ok := call(stores, users, stores.byID["ws-new"], "user-1")
		if !ok || rec.Code != http.StatusOK {
			t.Fatalf("most-recent workspace: ok=%v status=%d, want allowed", ok, rec.Code)
		}
	})

	t.Run("free over limit excess is read-only", func(t *testing.T) {
		stores, users := build(domain.TierFree,
			wsAt("ws-old", "user-1", older), wsAt("ws-new", "user-1", now))
		rec, ok := call(stores, users, stores.byID["ws-old"], "user-1")
		if ok || rec.Code != http.StatusForbidden {
			t.Fatalf("excess workspace: ok=%v status=%d, want 403", ok, rec.Code)
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decoding body: %v", err)
		}
		if body["code"] != "workspace_read_only_downgraded" {
			t.Errorf("code = %q, want workspace_read_only_downgraded", body["code"])
		}
	})

	t.Run("pro at limit allowed", func(t *testing.T) {
		stores, users := build(domain.TierPro,
			wsAt("ws-1", "user-1", now), wsAt("ws-2", "user-1", older), wsAt("ws-3", "user-1", older.Add(-time.Hour)),
			wsAt("ws-4", "user-1", older.Add(-2*time.Hour)), wsAt("ws-5", "user-1", older.Add(-3*time.Hour)))
		rec, ok := call(stores, users, stores.byID["ws-3"], "user-1")
		if !ok || rec.Code != http.StatusOK {
			t.Fatalf("pro at 5/5: ok=%v status=%d, want allowed", ok, rec.Code)
		}
	})

	t.Run("pro over limit gates excess", func(t *testing.T) {
		// A Team→Pro downgrade leaves 6 workspaces behind a 5-workspace Pro ceiling: only the most
		// recently updated stays writable.
		stores, users := build(domain.TierPro,
			wsAt("ws-recent", "user-1", now),
			wsAt("ws-excess", "user-1", older),
			wsAt("ws-2", "user-1", older.Add(-time.Hour)),
			wsAt("ws-3", "user-1", older.Add(-2*time.Hour)),
			wsAt("ws-4", "user-1", older.Add(-3*time.Hour)),
			wsAt("ws-5", "user-1", older.Add(-4*time.Hour)))
		rec, ok := call(stores, users, stores.byID["ws-excess"], "user-1")
		if ok || rec.Code != http.StatusForbidden {
			t.Fatalf("pro over limit: ok=%v status=%d, want 403", ok, rec.Code)
		}
		// The most recent workspace remains writable even over the limit.
		rec, ok = call(stores, users, stores.byID["ws-recent"], "user-1")
		if !ok || rec.Code != http.StatusOK {
			t.Fatalf("pro over limit most-recent: ok=%v status=%d, want allowed", ok, rec.Code)
		}
	})

	t.Run("team unlimited never gated", func(t *testing.T) {
		stores, users := build(domain.TierTeam,
			wsAt("ws-1", "user-1", now), wsAt("ws-2", "user-1", older), wsAt("ws-3", "user-1", older.Add(-time.Hour)))
		for id := range stores.byID {
			rec, ok := call(stores, users, stores.byID[id], "user-1")
			if !ok || rec.Code != http.StatusOK {
				t.Fatalf("team workspace %s: ok=%v status=%d, want allowed", id, ok, rec.Code)
			}
		}
	})

	t.Run("nil tier seam degrades to allowed", func(t *testing.T) {
		stores, _ := build(domain.TierFree, wsAt("ws-1", "user-1", now), wsAt("ws-2", "user-1", older))
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/whatever", nil)
		if ok := ownerMutationAllowedByTier(rec, req, stores, nil, stores.byID["ws-2"], "user-1"); !ok {
			t.Fatal("nil tier seam must degrade to allowed (test server with zero-value Dependencies)")
		}
	})
}

// TestMostRecentlyUpdatedWorkspace pins the recency tiebreak: the workspace with the newest
// UpdatedAt wins, matching the workspace list's ORDER BY updated_at DESC.
func TestMostRecentlyUpdatedWorkspace(t *testing.T) {
	now := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	list := []domain.Workspace{
		wsAt("a", "u", now.Add(-2*time.Hour)),
		wsAt("b", "u", now),
		wsAt("c", "u", now.Add(-time.Hour)),
	}
	if got := mostRecentlyUpdatedWorkspace(list); got != "b" {
		t.Errorf("mostRecentlyUpdatedWorkspace = %q, want b", got)
	}
	if got := mostRecentlyUpdatedWorkspace(nil); got != "" {
		t.Errorf("mostRecentlyUpdatedWorkspace(nil) = %q, want empty", got)
	}
}

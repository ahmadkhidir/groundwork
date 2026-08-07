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

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeMemberStore is an in-memory MemberStore. It stores the enriched MemberWithUser rows directly,
// with email/display name derived from the user id so the list endpoint has something to render.
type fakeMemberStore struct {
	byWorkspace map[string][]postgres.MemberWithUser
}

func newFakeMemberStore() *fakeMemberStore {
	return &fakeMemberStore{byWorkspace: make(map[string][]postgres.MemberWithUser)}
}

func (f *fakeMemberStore) Create(_ context.Context, member domain.WorkspaceMember) error {
	f.byWorkspace[member.WorkspaceID] = append(f.byWorkspace[member.WorkspaceID], postgres.MemberWithUser{
		Member:      member,
		Email:       member.UserID + "@example.com",
		DisplayName: "Member " + member.UserID,
	})
	return nil
}

func (f *fakeMemberStore) ListByWorkspaceWithUsers(_ context.Context, workspaceID string) ([]postgres.MemberWithUser, error) {
	return f.byWorkspace[workspaceID], nil
}

func (f *fakeMemberStore) Revoke(_ context.Context, id, workspaceID string) error {
	list := f.byWorkspace[workspaceID]
	for i, mw := range list {
		if mw.Member.ID == id {
			f.byWorkspace[workspaceID] = append(list[:i], list[i+1:]...)
			return nil
		}
	}
	return postgres.ErrMemberNotFound
}

// fakeUserByEmailStore is an in-memory UserByEmailStore.
type fakeUserByEmailStore struct {
	byEmail map[string]domain.User
}

func (f *fakeUserByEmailStore) FindByEmail(_ context.Context, email string) (domain.User, string, error) {
	user, ok := f.byEmail[email]
	if !ok {
		return domain.User{}, "", auth.ErrUserNotFound
	}
	return user, "hash", nil
}

// fakeMemberLookupStore is an in-memory MemberLookupStore, keyed by user id.
type fakeMemberLookupStore struct {
	byUser map[string]domain.WorkspaceMember
}

func newFakeMemberLookupStore() *fakeMemberLookupStore {
	return &fakeMemberLookupStore{byUser: make(map[string]domain.WorkspaceMember)}
}

func (f *fakeMemberLookupStore) GetByUserAndWorkspace(_ context.Context, userID, _ string) (domain.WorkspaceMember, error) {
	member, ok := f.byUser[userID]
	if !ok {
		return domain.WorkspaceMember{}, postgres.ErrMemberNotFound
	}
	return member, nil
}

// newTestMemberRouter wires RegisterMemberRoutes onto a fresh authenticated chi router, mirroring
// how api.go mounts protected routes.
func newTestMemberRouter(
	workspaces WorkspaceStore,
	members MemberStore,
	users UserByEmailStore,
	tiers UserTierLookup,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Use(authsession.RequireAuth(testAuthSecret))
		RegisterMemberRoutes(r, workspaces, members, users, tiers, slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	return r
}

// newMemberTestFixtures returns the member router plus its fakes, pre-seeded with a workspace owned
// by user-1.
func newMemberTestFixtures(t *testing.T) (http.Handler, *fakeWorkspaceStore, *fakeMemberStore, *fakeUserByEmailStore, *fakeUserTierLookup) {
	t.Helper()
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1", Name: "Q3 Numbers"}
	members := newFakeMemberStore()
	users := &fakeUserByEmailStore{byEmail: make(map[string]domain.User)}
	tiers := newFakeUserTierLookup()
	tiers.tiers["user-1"] = domain.TierTeam
	return newTestMemberRouter(workspaces, members, users, tiers), workspaces, members, users, tiers
}

func TestInviteMemberRequiresAuthentication(t *testing.T) {
	router, _, _, _, _ := newMemberTestFixtures(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/workspaces/ws-1/members", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestInviteMemberSucceedsOnTeamTier is FEAT-045 AC1 at the API layer: the owner can invite another
// account with read_only access, and the resulting member row carries the read_only role.
func TestInviteMemberSucceedsOnTeamTier(t *testing.T) {
	router, _, members, users, _ := newMemberTestFixtures(t)
	users.byEmail["ngozi@example.com"] = domain.User{ID: "user-2", Email: "ngozi@example.com", DisplayName: "Ngozi"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "ngozi@example.com"}, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Member publicMember `json:"member"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Member.Role != string(domain.RoleReadOnly) {
		t.Errorf("role = %q, want %q (every invite is read_only)", body.Member.Role, domain.RoleReadOnly)
	}
	if body.Member.Email != "ngozi@example.com" {
		t.Errorf("email = %q, want the invited email", body.Member.Email)
	}

	if len(members.byWorkspace["ws-1"]) != 1 {
		t.Fatalf("stored members = %d, want 1", len(members.byWorkspace["ws-1"]))
	}
}

// TestInviteMemberRejectsNonTeamTier verifies monetization.md's FEAT-045 gate (Team only) is
// enforced server-side against the authoritative tier, not the session cookie's claim.
func TestInviteMemberRejectsNonTeamTier(t *testing.T) {
	cases := []struct {
		name string
		tier domain.SubscriptionTier
	}{
		{"free tier", domain.TierFree},
		{"pro tier", domain.TierPro},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router, _, _, users, tiers := newMemberTestFixtures(t)
			users.byEmail["ngozi@example.com"] = domain.User{ID: "user-2", Email: "ngozi@example.com"}
			tiers.tiers["user-1"] = tc.tier

			req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "ngozi@example.com"}, "user-1", tc.tier)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decoding response: %v", err)
			}
			if body["code"] != "upgrade_required" {
				t.Errorf("code = %q, want upgrade_required", body["code"])
			}
		})
	}
}

func TestInviteMemberRejectsUnknownEmail(t *testing.T) {
	router, _, _, _, _ := newMemberTestFixtures(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "nobody@example.com"}, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestInviteMemberRejectsOwnerEmail(t *testing.T) {
	router, _, _, users, _ := newMemberTestFixtures(t)
	users.byEmail["owner@example.com"] = domain.User{ID: "user-1", Email: "owner@example.com"}

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "owner@example.com"}, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (inviting the owner would fabricate a revocable owner membership)", rec.Code, http.StatusBadRequest)
	}
}

func TestInviteMemberRequiresEmail(t *testing.T) {
	router, _, _, _, _ := newMemberTestFixtures(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "   "}, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// TestInviteMemberNonOwnerNotFound exercises the shared non-enumeration stance: a non-owner who
// POSTs an invite gets the same 404 as a nonexistent workspace.
func TestInviteMemberNonOwnerNotFound(t *testing.T) {
	router, _, _, _, _ := newMemberTestFixtures(t)

	req := authedRequest(t, http.MethodPost, "/v1/workspaces/ws-1/members", map[string]string{"email": "ngozi@example.com"}, "someone-else", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestListMembersOwnerOnly(t *testing.T) {
	router, _, members, _, _ := newMemberTestFixtures(t)
	members.Create(context.Background(), domain.WorkspaceMember{
		ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly, InvitedAt: time.Now().UTC(),
	})

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/members", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Members []publicMember `json:"members"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if len(body.Members) != 1 || body.Members[0].UserID != "user-2" {
		t.Errorf("members = %+v, want exactly [user-2]", body.Members)
	}

	// A read-only member must NOT be able to list (or manage) memberships — screens/13 hides
	// sharing management entirely for non-owners.
	req = authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1/members", nil, "user-2", domain.TierTeam)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("read-only member list status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRevokeMemberSucceeds(t *testing.T) {
	router, _, members, _, _ := newMemberTestFixtures(t)
	members.Create(context.Background(), domain.WorkspaceMember{
		ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly, InvitedAt: time.Now().UTC(),
	})

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/members/m-1", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(members.byWorkspace["ws-1"]) != 0 {
		t.Errorf("stored members = %d, want 0 after revoke", len(members.byWorkspace["ws-1"]))
	}
}

func TestRevokeMemberNotFound(t *testing.T) {
	router, _, _, _, _ := newMemberTestFixtures(t)

	req := authedRequest(t, http.MethodDelete, "/v1/workspaces/ws-1/members/does-not-exist", nil, "user-1", domain.TierTeam)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// ------------------------------------------------------------------------------------------------
// requireWorkspaceAccess / requireWorkspaceMutationAccess — the FEAT-045 enforcement helpers.
// ------------------------------------------------------------------------------------------------

func TestRequireWorkspaceAccessAllowsReadOnlyMember(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	members := newFakeMemberLookupStore()
	members.byUser["user-2"] = domain.WorkspaceMember{ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly}

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "user-2", domain.TierFree)
	rec := httptest.NewRecorder()

	ws, role, ok := requireWorkspaceAccess(rec, req, workspaces, members, "user-2", "ws-1")
	if !ok {
		t.Fatalf("read-only member denied access; body = %s", rec.Body.String())
	}
	if ws.ID != "ws-1" {
		t.Errorf("workspace id = %q, want ws-1", ws.ID)
	}
	if role != domain.RoleReadOnly {
		t.Errorf("role = %q, want read_only", role)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (a successful access check writes nothing)", rec.Code, http.StatusOK)
	}
}

func TestRequireWorkspaceAccessRejectsNonMember(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	members := newFakeMemberLookupStore()

	req := authedRequest(t, http.MethodGet, "/v1/workspaces/ws-1", nil, "stranger", domain.TierFree)
	rec := httptest.NewRecorder()

	_, _, ok := requireWorkspaceAccess(rec, req, workspaces, members, "stranger", "ws-1")
	if ok {
		t.Fatal("stranger granted access; want the non-enumeration 404")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestRequireWorkspaceMutationAccessAllowsOwner(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	members := newFakeMemberLookupStore()

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()

	_, ok := requireWorkspaceMutationAccess(rec, req, workspaces, members, newFakeUserTierLookup(), "user-1", "ws-1")
	if !ok {
		t.Fatalf("owner denied mutation access; body = %s", rec.Body.String())
	}
}

// TestRequireWorkspaceMutationAccessBlocksReadOnlyMember is FEAT-045's server-side enforcement
// contract (architecture.md): a read-only member attempting a mutation gets a distinct 403, not the
// non-owner 404 — they ARE granted access, just not write access, and the client must be able to
// show the honest "read-only" treatment.
func TestRequireWorkspaceMutationAccessBlocksReadOnlyMember(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	members := newFakeMemberLookupStore()
	members.byUser["user-2"] = domain.WorkspaceMember{ID: "m-1", WorkspaceID: "ws-1", UserID: "user-2", Role: domain.RoleReadOnly}

	req := authedRequest(t, http.MethodPatch, "/v1/workspaces/ws-1", nil, "user-2", domain.TierFree)
	rec := httptest.NewRecorder()

	_, ok := requireWorkspaceMutationAccess(rec, req, workspaces, members, newFakeUserTierLookup(), "user-2", "ws-1")
	if ok {
		t.Fatal("read-only member granted mutation access")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body["code"] != "read_only_member" {
		t.Errorf("code = %q, want read_only_member", body["code"])
	}
}

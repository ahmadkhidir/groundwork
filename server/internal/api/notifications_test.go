package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// fakeNotificationStore is an in-memory NotificationStore mirroring
// postgres.NotificationRepository's contract closely enough to exercise the handlers (scoping,
// ordering, pagination envelope, acknowledgment) without a real database. The cursor is opaque to
// the API layer, so the fake models "a cursor means one page was already consumed" rather than
// reimplementing the repository's keyset decode.
type fakeNotificationStore struct {
	byUser map[string][]domain.Notification
	// links is what ResolveLinks returns, keyed by notification id.
	links map[string]string
	// err, when set, is returned by every method — used to exercise the 500/400 paths.
	err error
	// lastPageSize records the pageSize the handler actually passed through, for the clamp test.
	lastPageSize int
}

func newFakeNotificationStore() *fakeNotificationStore {
	return &fakeNotificationStore{byUser: make(map[string][]domain.Notification), links: make(map[string]string)}
}

func (f *fakeNotificationStore) ListByUser(ctx context.Context, userID string, pageSize int, cursor string) (postgres.NotificationPage, error) {
	if f.err != nil {
		return postgres.NotificationPage{}, f.err
	}
	f.lastPageSize = pageSize

	all := append([]domain.Notification(nil), f.byUser[userID]...)
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Read != all[j].Read {
			return !all[i].Read // unread first
		}
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})

	start := 0
	if cursor != "" {
		start = pageSize // one page already consumed
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}

	page := all[start:end]
	next := ""
	if end < len(all) {
		next = "next-cursor"
	}
	return postgres.NotificationPage{Notifications: page, NextCursor: next}, nil
}

func (f *fakeNotificationStore) CountUnread(ctx context.Context, userID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	count := 0
	for _, n := range f.byUser[userID] {
		if !n.Read {
			count++
		}
	}
	return count, nil
}

func (f *fakeNotificationStore) MarkRead(ctx context.Context, userID, id string) error {
	if f.err != nil {
		return f.err
	}
	for i, n := range f.byUser[userID] {
		if n.ID == id {
			f.byUser[userID][i].Read = true
			return nil
		}
	}
	return postgres.ErrNotificationNotFound
}

func (f *fakeNotificationStore) MarkAllRead(ctx context.Context, userID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	count := 0
	for i := range f.byUser[userID] {
		if !f.byUser[userID][i].Read {
			f.byUser[userID][i].Read = true
			count++
		}
	}
	return count, nil
}

func (f *fakeNotificationStore) ResolveLinks(ctx context.Context, notifications []domain.Notification) (map[string]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make(map[string]string, len(notifications))
	for _, n := range notifications {
		if ws, ok := f.links[n.ID]; ok {
			out[n.ID] = ws
		}
	}
	return out, nil
}

func testNotification(id, userID string, read bool, createdAt time.Time) domain.Notification {
	return domain.Notification{
		ID:                id,
		UserID:            userID,
		Type:              domain.NotifyExportReady,
		Message:           "message-" + id,
		RelatedEntityType: domain.RelatedReport,
		RelatedEntityID:   "report-" + id,
		Read:              read,
		CreatedAt:         createdAt,
	}
}

// newNotificationTestRouter builds a router with RequireAuth already applied, exactly as api.go's
// protected group mounts it, so the tests exercise the real auth boundary and the real handlers
// without needing to modify the read-only Dependencies struct.
func newNotificationTestRouter(t *testing.T, store NotificationStore, users *fakeUserTierLookup) http.Handler {
	t.Helper()
	mux := chi.NewRouter()
	mux.Use(authsession.RequireAuth(testAuthSecret))
	RegisterNotificationRoutes(mux, store, users, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mux
}

func TestNotificationsRequireAuthentication(t *testing.T) {
	mux := newNotificationTestRouter(t, newFakeNotificationStore(), newFakeUserTierLookup())

	req := httptest.NewRequest(http.MethodGet, "/notifications", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestNotificationsFreeTierGetsUpgradeRequired(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	// users.tiers left empty → FindByID defaults to Free, the authoritative read.
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "upgrade_required" {
		t.Errorf("code = %q, want upgrade_required", body["code"])
	}
}

func TestNotificationsFreeTierGateAppliesToAcknowledge(t *testing.T) {
	store := newFakeNotificationStore()
	mux := newNotificationTestRouter(t, store, newFakeUserTierLookup())

	req := authedRequest(t, http.MethodPatch, "/notifications/n1", map[string]bool{"read": true}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestNotificationsListUnreadFirstWithCount(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	now := time.Now()
	store.byUser["user-1"] = []domain.Notification{
		testNotification("n-read-1", "user-1", true, now.Add(-2*time.Hour)),
		testNotification("n-unread-2", "user-1", false, now.Add(-1*time.Hour)),
		testNotification("n-unread-1", "user-1", false, now.Add(-time.Minute)),
	}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Notifications []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"notifications"`
		UnreadCount int    `json:"unread_count"`
		NextCursor  string `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	// The handler preserves the store's unread-first, newest-first ordering — it never re-sorts.
	got := make([]string, 0, len(body.Notifications))
	for _, n := range body.Notifications {
		got = append(got, n.ID)
	}
	want := []string{"n-unread-1", "n-unread-2", "n-read-1"}
	if len(got) != len(want) {
		t.Fatalf("notifications = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("notifications = %v, want %v", got, want)
		}
	}
	if body.UnreadCount != 2 {
		t.Errorf("unread_count = %d, want 2", body.UnreadCount)
	}
	if body.NextCursor != "" {
		t.Errorf("next_cursor = %q, want empty for a list under the page size", body.NextCursor)
	}
}

func TestNotificationsUnreadCountOnlyVariant(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{
		testNotification("n1", "user-1", false, time.Now()),
		testNotification("n2", "user-1", false, time.Now()),
		testNotification("n3", "user-1", true, time.Now()),
	}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications?unread_count=true", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["unread_count"] != float64(2) {
		t.Errorf("unread_count = %v, want 2", body["unread_count"])
	}
	if _, ok := body["notifications"]; ok {
		t.Error("unread_count=true must not fetch the list")
	}
}

// FEAT-026 AC1: "Durable notifications persist across sessions until acknowledged." A fresh GET
// after a PATCH is a new "session" — the acknowledgment must have persisted, and the read
// notification must remain visible (history retained, AC3).
func TestNotificationsAcknowledgePersistsAcrossSessionsAndHistoryRetained(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{
		testNotification("n1", "user-1", false, time.Now().Add(-time.Hour)),
		testNotification("n2", "user-1", true, time.Now().Add(-2*time.Hour)),
	}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodPatch, "/notifications/n1", map[string]bool{"read": true}, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("acknowledge status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	req = authedRequest(t, http.MethodGet, "/notifications", nil, "user-1", domain.TierPro)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body struct {
		Notifications []struct {
			ID   string `json:"id"`
			Read bool   `json:"read"`
		} `json:"notifications"`
		UnreadCount int `json:"unread_count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}

	// n1 is now read but still present — history is retained, not deleted on acknowledgment.
	seen := make(map[string]bool)
	for _, n := range body.Notifications {
		seen[n.ID] = n.Read
	}
	if !seen["n1"] {
		t.Error("n1 missing after acknowledgment; history must be retained")
	}
	if body.UnreadCount != 0 {
		t.Errorf("unread_count = %d, want 0 after acknowledging the only unread", body.UnreadCount)
	}
}

// FEAT-026 user isolation: notifications are per-user; another user's rows must never appear, and
// acknowledging one must never touch another user's rows.
func TestNotificationsUserIsolation(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	users.tiers["user-2"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{testNotification("n1", "user-1", false, time.Now())}
	store.byUser["user-2"] = []domain.Notification{testNotification("n2", "user-2", false, time.Now())}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body struct {
		Notifications []struct {
			ID     string `json:"id"`
			UserID string `json:"user_id"`
		} `json:"notifications"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.Notifications) != 1 || body.Notifications[0].ID != "n1" {
		t.Fatalf("notifications = %+v, want only user-1's n1", body.Notifications)
	}

	// A user-2 acknowledge of user-1's notification must 404 (non-enumeration), not succeed.
	req = authedRequest(t, http.MethodPatch, "/notifications/n1", nil, "user-2", domain.TierPro)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user acknowledge status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestNotificationsMarkAllRead(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{
		testNotification("n1", "user-1", false, time.Now()),
		testNotification("n2", "user-1", false, time.Now()),
		testNotification("n3", "user-1", true, time.Now()),
	}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodPost, "/notifications/read-all", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["marked_read"] != float64(2) {
		t.Errorf("marked_read = %v, want 2", body["marked_read"])
	}

	// A subsequent list shows everything read.
	req = authedRequest(t, http.MethodGet, "/notifications?unread_count=true", nil, "user-1", domain.TierPro)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["unread_count"] != float64(0) {
		t.Errorf("unread_count = %v, want 0", body["unread_count"])
	}
}

func TestNotificationsResolvedWorkspaceLink(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{
		testNotification("n-live", "user-1", false, time.Now()),
		testNotification("n-deleted", "user-1", false, time.Now()),
	}
	// n-live's report exists in workspace ws-1; n-deleted's entity is gone (no map entry).
	store.links["n-live"] = "ws-1"
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body struct {
		Notifications []map[string]any `json:"notifications"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	byID := make(map[string]map[string]any)
	for _, n := range body.Notifications {
		byID[n["id"].(string)] = n
	}
	if ws, ok := byID["n-live"]["workspace_id"]; !ok || ws != "ws-1" {
		t.Errorf("n-live workspace_id = %v, want ws-1", byID["n-live"]["workspace_id"])
	}
	if _, ok := byID["n-deleted"]["workspace_id"]; ok {
		t.Error("n-deleted must not carry workspace_id — its linked entity is gone")
	}
}

func TestNotificationsLimitClampedToMax(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications?limit=1000", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if store.lastPageSize != maxNotificationPageSize {
		t.Errorf("pageSize passed to store = %d, want clamped %d", store.lastPageSize, maxNotificationPageSize)
	}
}

func TestNotificationsInvalidCursorReturns400(t *testing.T) {
	store := newFakeNotificationStore()
	store.err = postgres.ErrInvalidCursor
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodGet, "/notifications?cursor=garbage", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestNotificationsAcknowledgeRejectsUnreadRequest(t *testing.T) {
	store := newFakeNotificationStore()
	users := newFakeUserTierLookup()
	users.tiers["user-1"] = domain.TierPro
	store.byUser["user-1"] = []domain.Notification{testNotification("n1", "user-1", false, time.Now())}
	mux := newNotificationTestRouter(t, store, users)

	req := authedRequest(t, http.MethodPatch, "/notifications/n1", map[string]bool{"read": false}, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// ---- user-scoped deletion fakes ------------------------------------------------

// decodeAccountTestJSON decodes a recorded response body into dest, failing the test on error.
func decodeAccountTestJSON(t *testing.T, rec *httptest.ResponseRecorder, dest any) {
	t.Helper()
	if err := json.NewDecoder(rec.Body).Decode(dest); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
}

type fakeUserScopedDeleter struct {
	mu      sync.Mutex
	deleted []string
	err     error
}

func (f *fakeUserScopedDeleter) DeleteByUser(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, userID)
	return nil
}

type fakeAccountUserDeleter struct {
	mu      sync.Mutex
	deleted []string
	err     error
}

func (f *fakeAccountUserDeleter) Delete(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, userID)
	return nil
}

// fakeResetTokenInvalidator records the user whose outstanding reset tokens were destroyed.
type fakeResetTokenInvalidator struct {
	mu      sync.Mutex
	deleted []string
	err     error
}

func (f *fakeResetTokenInvalidator) InvalidateForUser(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, userID)
	return nil
}

// newTestAccountDeleteRouter mirrors api.go's wiring for the FEAT-052 account route.
func newTestAccountDeleteRouter(
	workspaces *fakeWorkspaceStore,
	objects *fakeWorkspaceObjectDeleter,
	engine *fakeWorkspaceEngineDropper,
	cascade *fakeWorkspaceDeletionStore,
	auditWriter *fakeWorkspaceAuditWriter,
	members *fakeUserScopedDeleter,
	notifications *fakeUserScopedDeleter,
	resets *fakeResetTokenInvalidator,
	audit *fakeUserScopedDeleter,
	billing *fakeBillingUserStore,
	billingClient *fakeStripeClient,
	billingEvents *fakeUserScopedDeleter,
	users *fakeAccountUserDeleter,
) http.Handler {
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			RegisterAccountDeleteRoute(r, workspaces, objects, engine, cascade, auditWriter,
				members, notifications, resets, audit, billing, billingClient, billingEvents, users,
				slog.New(slog.NewTextHandler(io.Discard, nil)))
		})
	})
	return r
}

func TestDeleteAccountHappyPath(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	workspaces.byID["ws-2"] = domain.Workspace{ID: "ws-2", OwnerID: "user-1"}
	objects := &fakeWorkspaceObjectDeleter{}
	engine := &fakeWorkspaceEngineDropper{}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	auditWriter := &fakeWorkspaceAuditWriter{}
	members := &fakeUserScopedDeleter{}
	notifications := &fakeUserScopedDeleter{}
	resets := &fakeResetTokenInvalidator{}
	audit := &fakeUserScopedDeleter{}
	billing := newFakeBillingUserStore()
	billing.seed(postgres.UserBilling{
		UserID:               "user-1",
		Tier:                 domain.TierPro,
		StripeCustomerID:     "cus-1",
		StripeSubscriptionID: "sub-1",
	})
	billingClient := &fakeStripeClient{}
	billingEvents := &fakeUserScopedDeleter{}
	users := &fakeAccountUserDeleter{}

	router := newTestAccountDeleteRouter(workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit, billing, billingClient, billingEvents, users)

	req := authedRequest(t, http.MethodDelete, "/v1/account", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	// Both owned workspaces went through the full cascade (rows gone, proofs written).
	if _, ok := workspaces.byID["ws-1"]; ok {
		t.Error("ws-1 still present after account deletion")
	}
	if _, ok := workspaces.byID["ws-2"]; ok {
		t.Error("ws-2 still present after account deletion")
	}
	gotPrefixes := objects.deletedPrefixes()
	if len(gotPrefixes) != 2 || gotPrefixes[0] != "workspaces/ws-1/" || gotPrefixes[1] != "workspaces/ws-2/" {
		t.Errorf("object prefixes = %v, want ws-1 then ws-2", gotPrefixes)
	}
	gotDropped := engine.droppedWorkspaces()
	if len(gotDropped) != 2 {
		t.Errorf("engine drops = %v, want both workspaces", gotDropped)
	}
	if auditWriter.count() != 2 {
		t.Errorf("workspace deletion audit writes = %d, want 2", auditWriter.count())
	}

	// User-scoped cleanup ran.
	for name, f := range map[string]*fakeUserScopedDeleter{
		"members": members, "notifications": notifications, "audit": audit, "billingEvents": billingEvents,
	} {
		if len(f.deleted) != 1 || f.deleted[0] != "user-1" {
			t.Errorf("%s delete = %v, want [user-1]", name, f.deleted)
		}
	}
	if len(resets.deleted) != 1 || resets.deleted[0] != "user-1" {
		t.Errorf("resets invalidated = %v, want [user-1]", resets.deleted)
	}

	// Stripe: subscription cancelled immediately + customer deleted.
	if got, ok := billingClient.lastCancelledSubscription(); !ok || got != "sub-1" {
		t.Errorf("cancelled subscription = %q/%v, want sub-1", got, ok)
	}
	if got, ok := billingClient.lastDeletedCustomer(); !ok || got != "cus-1" {
		t.Errorf("deleted customer = %q/%v, want cus-1", got, ok)
	}

	// The users row fell LAST.
	if len(users.deleted) != 1 || users.deleted[0] != "user-1" {
		t.Errorf("users delete = %v, want [user-1]", users.deleted)
	}
}

func TestDeleteAccountWorkspaceFailureAbortsWithAccountIntact(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	workspaces.byID["ws-1"] = domain.Workspace{ID: "ws-1", OwnerID: "user-1"}
	objects := &fakeWorkspaceObjectDeleter{failPrefix: "workspaces/ws-1/"}
	engine := &fakeWorkspaceEngineDropper{}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	auditWriter := &fakeWorkspaceAuditWriter{}
	members := &fakeUserScopedDeleter{}
	notifications := &fakeUserScopedDeleter{}
	resets := &fakeResetTokenInvalidator{}
	audit := &fakeUserScopedDeleter{}
	billing := newFakeBillingUserStore()
	billingClient := &fakeStripeClient{}
	billingEvents := &fakeUserScopedDeleter{}
	users := &fakeAccountUserDeleter{}

	router := newTestAccountDeleteRouter(workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit, billing, billingClient, billingEvents, users)

	req := authedRequest(t, http.MethodDelete, "/v1/account", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code          string `json:"code"`
		FailedStep    string `json:"failed_step"`
		AccountIntact bool   `json:"account_intact"`
	}
	decodeAccountTestJSON(t, rec, &body)
	if body.Code != "account_deletion_partial_failed" || !body.AccountIntact {
		t.Errorf("body = %+v, want partial-failure with account intact", body)
	}
	if body.FailedStep == "" {
		t.Error("failed_step is empty")
	}
	// The workspace cascade failed BEFORE the workspace row fell — the account and workspace survive.
	if _, ok := workspaces.byID["ws-1"]; !ok {
		t.Error("workspace was deleted despite the failed cascade")
	}
	if len(users.deleted) != 0 {
		t.Error("users row deleted despite the failed cascade")
	}
	if len(members.deleted) != 0 || len(notifications.deleted) != 0 {
		t.Error("user-scoped cleanup ran despite the failed workspace cascade")
	}
}

func TestDeleteAccountStripeFailureAbortsWithAccountIntact(t *testing.T) {
	workspaces := newFakeWorkspaceStore()
	objects := &fakeWorkspaceObjectDeleter{}
	engine := &fakeWorkspaceEngineDropper{}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	auditWriter := &fakeWorkspaceAuditWriter{}
	members := &fakeUserScopedDeleter{}
	notifications := &fakeUserScopedDeleter{}
	resets := &fakeResetTokenInvalidator{}
	audit := &fakeUserScopedDeleter{}
	billing := newFakeBillingUserStore()
	billing.seed(postgres.UserBilling{UserID: "user-1", Tier: domain.TierPro, StripeSubscriptionID: "sub-1"})
	billingClient := &fakeStripeClient{cancelErr: errors.New("stripe is down")}
	billingEvents := &fakeUserScopedDeleter{}
	users := &fakeAccountUserDeleter{}

	router := newTestAccountDeleteRouter(workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit, billing, billingClient, billingEvents, users)

	req := authedRequest(t, http.MethodDelete, "/v1/account", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Code          string `json:"code"`
		FailedStep    string `json:"failed_step"`
		AccountIntact bool   `json:"account_intact"`
	}
	decodeAccountTestJSON(t, rec, &body)
	if body.FailedStep != "stripe_cancel" || !body.AccountIntact {
		t.Errorf("body = %+v, want stripe_cancel failure with account intact", body)
	}
	if len(users.deleted) != 0 {
		t.Error("users row deleted despite the Stripe failure")
	}
}

func TestDeleteAccountSkipsAlreadyDeletedWorkspace(t *testing.T) {
	// A prior attempt already destroyed ws-1 (the workspace row is gone); the retry must treat it
	// as done and continue, not fail.
	workspaces := newFakeWorkspaceStore()
	objects := &fakeWorkspaceObjectDeleter{}
	engine := &fakeWorkspaceEngineDropper{}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	auditWriter := &fakeWorkspaceAuditWriter{}
	members := &fakeUserScopedDeleter{}
	notifications := &fakeUserScopedDeleter{}
	resets := &fakeResetTokenInvalidator{}
	audit := &fakeUserScopedDeleter{}
	billing := newFakeBillingUserStore()
	billingClient := &fakeStripeClient{}
	billingEvents := &fakeUserScopedDeleter{}
	users := &fakeAccountUserDeleter{}

	router := newTestAccountDeleteRouter(workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit, billing, billingClient, billingEvents, users)

	req := authedRequest(t, http.MethodDelete, "/v1/account", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 on a retry with an already-deleted workspace; body: %s", rec.Code, rec.Body.String())
	}
	if len(users.deleted) != 1 {
		t.Errorf("users delete = %v, want the account deleted on retry", users.deleted)
	}
}

func TestDeleteAccountAlreadyDeletedUserIsSuccess(t *testing.T) {
	// A stale cookie (the JWE is self-contained) retrying after a fully-completed deletion: the
	// users row is already gone — that IS the requested outcome.
	workspaces := newFakeWorkspaceStore()
	objects := &fakeWorkspaceObjectDeleter{}
	engine := &fakeWorkspaceEngineDropper{}
	cascade := newFakeWorkspaceDeletionStore(workspaces, newFakeDeletionProofStore())
	auditWriter := &fakeWorkspaceAuditWriter{}
	members := &fakeUserScopedDeleter{}
	notifications := &fakeUserScopedDeleter{}
	resets := &fakeResetTokenInvalidator{}
	audit := &fakeUserScopedDeleter{}
	billing := newFakeBillingUserStore()
	billingClient := &fakeStripeClient{}
	billingEvents := &fakeUserScopedDeleter{}
	users := &fakeAccountUserDeleter{err: auth.ErrUserNotFound}

	router := newTestAccountDeleteRouter(workspaces, objects, engine, cascade, auditWriter,
		members, notifications, resets, audit, billing, billingClient, billingEvents, users)

	req := authedRequest(t, http.MethodDelete, "/v1/account", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when the account is already gone; body: %s", rec.Code, rec.Body.String())
	}
}

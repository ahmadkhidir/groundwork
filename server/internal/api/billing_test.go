package api

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/authsession"
	"github.com/ahmadkhidir/groundwork/server/internal/billing"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

const (
	billingTestSecret    = "whsec_billing-test-secret"
	billingTestPricePro  = "price_pro_test"
	billingTestPriceTeam = "price_team_test"
	billingTestWebBase   = "http://localhost:3000"
)

// fakeBillingUserStore is an in-memory BillingUserStore.
type fakeBillingUserStore struct {
	mu         sync.Mutex
	byID       map[string]postgres.UserBilling
	byCustomer map[string]string // customer id -> user id
	tierCalls  []string          // user ids whose tier was updated, in order
}

func newFakeBillingUserStore() *fakeBillingUserStore {
	return &fakeBillingUserStore{
		byID:       make(map[string]postgres.UserBilling),
		byCustomer: make(map[string]string),
	}
}

func (f *fakeBillingUserStore) seed(user postgres.UserBilling) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byID[user.UserID] = user
	if user.StripeCustomerID != "" {
		f.byCustomer[user.StripeCustomerID] = user.UserID
	}
}

func (f *fakeBillingUserStore) GetBillingByID(ctx context.Context, userID string) (postgres.UserBilling, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[userID]
	if !ok {
		return postgres.UserBilling{}, auth.ErrUserNotFound
	}
	return u, nil
}

func (f *fakeBillingUserStore) GetBillingByStripeCustomerID(ctx context.Context, customerID string) (postgres.UserBilling, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	userID, ok := f.byCustomer[customerID]
	if !ok {
		return postgres.UserBilling{}, auth.ErrUserNotFound
	}
	return f.byID[userID], nil
}

func (f *fakeBillingUserStore) UpdateTier(ctx context.Context, userID string, tier domain.SubscriptionTier) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[userID]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.Tier = tier
	f.byID[userID] = u
	f.tierCalls = append(f.tierCalls, userID)
	return nil
}

func (f *fakeBillingUserStore) SetStripeCustomer(ctx context.Context, userID, customerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[userID]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.StripeCustomerID = customerID
	f.byID[userID] = u
	f.byCustomer[customerID] = userID
	return nil
}

func (f *fakeBillingUserStore) SetStripeSubscription(ctx context.Context, userID, subscriptionID, status string, periodEnd time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[userID]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.StripeSubscriptionID = subscriptionID
	u.StripeSubscriptionStatus = status
	u.StripeSubscriptionPeriodEnd = periodEnd
	f.byID[userID] = u
	return nil
}

// fakeBillingEventStore is an in-memory BillingEventStore: eventID -> present.
type fakeBillingEventStore struct {
	mu      sync.Mutex
	records map[string]bool
	inserts int
}

func newFakeBillingEventStore() *fakeBillingEventStore {
	return &fakeBillingEventStore{records: make(map[string]bool)}
}

func (f *fakeBillingEventStore) RecordIfNew(ctx context.Context, eventID, eventType, userID string, data []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inserts++
	if f.records[eventID] {
		return false, nil
	}
	f.records[eventID] = true
	return true, nil
}

func (f *fakeBillingEventStore) insertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inserts
}

func (f *fakeBillingEventStore) has(eventID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.records[eventID]
}

// fakeStripeClient is an in-memory StripeClient.
type fakeStripeClient struct {
	mu              sync.Mutex
	checkoutResult  billing.CheckoutResult
	checkoutErr     error
	checkoutParams  []billing.CheckoutParams
	snapshot        billing.SubscriptionSnapshot
	snapshotErr     error
	portalResult    billing.PortalSessionResult
	portalErr       error
	portalCustomers []string
	// FEAT-052 account deletion: records the subscription/customer the cascade cancelled/deleted,
	// and the error to return (nil by default).
	cancelSubscriptions []string
	cancelErr           error
	deletedCustomers    []string
	deleteErr           error
}

func (f *fakeStripeClient) CreateCheckoutSession(ctx context.Context, p billing.CheckoutParams) (billing.CheckoutResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkoutParams = append(f.checkoutParams, p)
	if f.checkoutErr != nil {
		return billing.CheckoutResult{}, f.checkoutErr
	}
	return f.checkoutResult, nil
}

func (f *fakeStripeClient) GetSubscription(ctx context.Context, id string) (billing.SubscriptionSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.snapshotErr != nil {
		return billing.SubscriptionSnapshot{}, f.snapshotErr
	}
	return f.snapshot, nil
}

func (f *fakeStripeClient) CreatePortalSession(ctx context.Context, customerID string) (billing.PortalSessionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portalCustomers = append(f.portalCustomers, customerID)
	if f.portalErr != nil {
		return billing.PortalSessionResult{}, f.portalErr
	}
	return f.portalResult, nil
}

func (f *fakeStripeClient) lastPortalCustomer() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.portalCustomers) == 0 {
		return "", false
	}
	return f.portalCustomers[len(f.portalCustomers)-1], true
}

func (f *fakeStripeClient) CancelSubscription(ctx context.Context, subscriptionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelSubscriptions = append(f.cancelSubscriptions, subscriptionID)
	return f.cancelErr
}

func (f *fakeStripeClient) DeleteCustomer(ctx context.Context, customerID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deletedCustomers = append(f.deletedCustomers, customerID)
	return f.deleteErr
}

func (f *fakeStripeClient) lastCancelledSubscription() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.cancelSubscriptions) == 0 {
		return "", false
	}
	return f.cancelSubscriptions[len(f.cancelSubscriptions)-1], true
}

func (f *fakeStripeClient) lastDeletedCustomer() (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.deletedCustomers) == 0 {
		return "", false
	}
	return f.deletedCustomers[len(f.deletedCustomers)-1], true
}

func (f *fakeStripeClient) lastCheckoutParams() billing.CheckoutParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.checkoutParams) == 0 {
		return billing.CheckoutParams{}
	}
	return f.checkoutParams[len(f.checkoutParams)-1]
}

// newBillingTestRouter mirrors the wiring the orchestrator will apply in api.go: the webhook route
// on the PUBLIC /v1 group, the checkout/subscription routes behind authsession.RequireAuth.
func newBillingTestRouter(users BillingUserStore, events BillingEventStore, client StripeClient, webhookSecret, pricePro, priceTeam, webBaseURL string) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		// Public: Stripe has no Auth.js session cookie.
		if events != nil && users != nil && client != nil {
			RegisterStripeWebhookRoute(r, users, events, client, webhookSecret, pricePro, priceTeam, logger)
		}
		r.Group(func(r chi.Router) {
			r.Use(authsession.RequireAuth(testAuthSecret))
			if users != nil && client != nil {
				RegisterBillingRoutes(r, users, client, pricePro, priceTeam, webBaseURL, logger)
			}
		})
	})
	return r
}

// signedWebhookRequest builds a POST to /v1/billing/webhook carrying a valid Stripe-Signature header
// for payload under secret. The webhook route is public, so no session cookie is attached.
func signedWebhookRequest(t *testing.T, payload []byte, secret string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhook", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	ts := time.Now()
	sig := webhook.ComputeSignature(ts, payload, secret)
	req.Header.Set("Stripe-Signature", "t="+strconv.FormatInt(ts.Unix(), 10)+",v1="+hex.EncodeToString(sig))
	return req
}

// billingEventPayload builds a Stripe webhook body for the given type and data object.
func billingEventPayload(t *testing.T, id, eventType string, data any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id":     id,
		"object": "event",
		"type":   eventType,
		"data":   map[string]any{"object": data},
	})
	if err != nil {
		t.Fatalf("marshaling event payload: %v", err)
	}
	return payload
}

// --- Auth boundary ---------------------------------------------------------------------------

func TestBillingSubscriptionRequiresAuthentication(t *testing.T) {
	handler := newBillingTestRouter(newFakeBillingUserStore(), newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := httptest.NewRequest(http.MethodGet, "/v1/billing/subscription", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestBillingCheckoutRequiresAuthentication(t *testing.T) {
	handler := newBillingTestRouter(newFakeBillingUserStore(), newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := httptest.NewRequest(http.MethodPost, "/v1/billing/checkout", bytes.NewReader([]byte(`{"plan":"pro"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// TestBillingWebhookRejectsUnsignedRequest pins that the webhook route is PUBLIC (no session cookie
// needed to reach it — Stripe has none) and that it self-authenticates via the signature: an
// unsigned request gets 400 invalid_signature, never 401.
func TestBillingWebhookRejectsUnsignedRequest(t *testing.T) {
	handler := newBillingTestRouter(newFakeBillingUserStore(), newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := httptest.NewRequest(http.MethodPost, "/v1/billing/webhook", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (public route, signature is the auth)", rec.Code, http.StatusBadRequest)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "invalid_signature" {
		t.Errorf("code = %q, want invalid_signature", body["code"])
	}
}

func TestBillingWebhookRejectsBadSignature(t *testing.T) {
	handler := newBillingTestRouter(newFakeBillingUserStore(), newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	payload := billingEventPayload(t, "evt_1", "checkout.session.completed", map[string]any{"id": "cs_1"})
	req := signedWebhookRequest(t, payload, "whsec_wrong-secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

// --- Checkout creation ------------------------------------------------------------------------

func TestBillingCheckoutValidatesPlan(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	for _, plan := range []string{"", "enterprise", "free", "PRO "} {
		// "PRO " trims+lowercases to pro → valid; the others must 400.
		if plan == "PRO " {
			continue
		}
		req := authedRequest(t, http.MethodPost, "/v1/billing/checkout", map[string]string{"plan": plan}, "user-1", domain.TierFree)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("plan %q: status = %d, want %d", plan, rec.Code, http.StatusBadRequest)
		}
	}
}

func TestBillingCheckoutHappyPath(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	client := &fakeStripeClient{checkoutResult: billing.CheckoutResult{SessionID: "cs_1", URL: "https://checkout.stripe.com/c/pay/cs_1"}}
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/checkout", map[string]string{"plan": "pro"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var body struct {
		Checkout struct {
			SessionID string `json:"session_id"`
			URL       string `json:"url"`
		} `json:"checkout"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Checkout.SessionID != "cs_1" || body.Checkout.URL != "https://checkout.stripe.com/c/pay/cs_1" {
		t.Errorf("checkout = %+v", body.Checkout)
	}

	p := client.lastCheckoutParams()
	if p.UserID != "user-1" || p.Email != "a@b.com" || p.Plan != domain.TierPro || p.PriceID != billingTestPricePro {
		t.Errorf("checkout params = %+v", p)
	}
	if p.SuccessURL != billingTestWebBase+upgradeSuccessPath {
		t.Errorf("success_url = %q, want %q", p.SuccessURL, billingTestWebBase+upgradeSuccessPath)
	}
	if p.CancelURL != billingTestWebBase+upgradeCancelledPath {
		t.Errorf("cancel_url = %q, want %q", p.CancelURL, billingTestWebBase+upgradeCancelledPath)
	}

	// The checkout call must NOT change the tier — that happens in the webhook.
	if got, _ := users.GetBillingByID(context.Background(), "user-1"); got.Tier != domain.TierFree {
		t.Errorf("tier after checkout = %q, want free (webhook applies the change)", got.Tier)
	}
}

func TestBillingCheckoutTeamPlan(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	client := &fakeStripeClient{checkoutResult: billing.CheckoutResult{SessionID: "cs_2", URL: "https://checkout.stripe.com/c/pay/cs_2"}}
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/checkout", map[string]string{"plan": "team"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if p := client.lastCheckoutParams(); p.Plan != domain.TierTeam || p.PriceID != billingTestPriceTeam {
		t.Errorf("checkout params = %+v, want team plan + team price", p)
	}
}

func TestBillingCheckoutPriceNotConfigured(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, "", billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/checkout", map[string]string{"plan": "pro"}, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body["code"] != "billing_not_configured" {
		t.Errorf("code = %q, want billing_not_configured", body["code"])
	}
}

// --- Webhook: checkout.session.completed ------------------------------------------------------

func TestBillingWebhookCheckoutCompletedUpgradesUser(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	events := newFakeBillingEventStore()
	client := &fakeStripeClient{snapshot: billing.SubscriptionSnapshot{
		ID: "sub_123", Status: "active", CurrentPeriodEnd: 1757000000, PriceID: billingTestPricePro,
	}}
	handler := newBillingTestRouter(users, events, client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{
		"id": "cs_1", "object": "checkout.session", "mode": "subscription",
		"client_reference_id": "user-1", "customer": "cus_123", "subscription": "sub_123",
		"metadata": map[string]string{"plan": "pro"},
	}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_checkout", "checkout.session.completed", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	got, err := users.GetBillingByID(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetBillingByID: %v", err)
	}
	if got.Tier != domain.TierPro {
		t.Errorf("tier = %q, want pro", got.Tier)
	}
	if got.StripeCustomerID != "cus_123" || got.StripeSubscriptionID != "sub_123" || got.StripeSubscriptionStatus != "active" {
		t.Errorf("stripe state = customer %q sub %q status %q", got.StripeCustomerID, got.StripeSubscriptionID, got.StripeSubscriptionStatus)
	}
	wantPeriod := time.Unix(1757000000, 0).UTC()
	if !got.StripeSubscriptionPeriodEnd.Equal(wantPeriod) {
		t.Errorf("period end = %v, want %v", got.StripeSubscriptionPeriodEnd, wantPeriod)
	}
	if !events.has("evt_checkout") {
		t.Error("checkout event was not recorded")
	}
}

func TestBillingWebhookCheckoutCompletedReplayIsIdempotent(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	events := newFakeBillingEventStore()
	events.records["evt_checkout"] = true // simulate a prior delivery
	client := &fakeStripeClient{snapshot: billing.SubscriptionSnapshot{ID: "sub_123", Status: "active", PriceID: billingTestPricePro}}
	handler := newBillingTestRouter(users, events, client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{
		"id": "cs_1", "object": "checkout.session", "mode": "subscription",
		"client_reference_id": "user-1", "customer": "cus_123", "subscription": "sub_123",
		"metadata": map[string]string{"plan": "pro"},
	}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_checkout", "checkout.session.completed", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	// The event was NOT recorded a second time — the idempotency guard held.
	if events.insertCount() != 1 {
		t.Errorf("RecordIfNew calls = %d, want exactly 1 (the replay must be a no-op)", events.insertCount())
	}
	// And the outcome is exactly the single application's outcome.
	got, _ := users.GetBillingByID(context.Background(), "user-1")
	if got.Tier != domain.TierPro || got.StripeSubscriptionID != "sub_123" {
		t.Errorf("replay changed state unexpectedly: %+v", got)
	}
}

func TestBillingWebhookCheckoutCompletedUnknownUser(t *testing.T) {
	users := newFakeBillingUserStore()
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{
		"id": "cs_1", "object": "checkout.session", "mode": "subscription",
		"client_reference_id": "ghost-user", "customer": "cus_999", "subscription": "sub_999",
		"metadata": map[string]string{"plan": "pro"},
	}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_ghost", "checkout.session.completed", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Acknowledged (recorded) so Stripe stops retrying; the event row is the audit trail for a refund.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !events.has("evt_ghost") {
		t.Error("unknown-user checkout event was not recorded")
	}
}

func TestBillingWebhookCheckoutCompletedMissingMetadata(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{
		"id": "cs_1", "object": "checkout.session", "mode": "subscription",
		"client_reference_id": "user-1", "customer": "cus_123", "subscription": "sub_123",
	}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_noplan", "checkout.session.completed", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// A genuine processing error: 500 WITHOUT recording, so Stripe retries and the event is not lost.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if events.has("evt_noplan") {
		t.Error("a failed checkout event must not be recorded (Stripe must retry)")
	}
	if got, _ := users.GetBillingByID(context.Background(), "user-1"); got.Tier != domain.TierFree {
		t.Error("tier changed despite the processing error")
	}
}

// --- Webhook: customer.subscription.deleted ---------------------------------------------------

func TestBillingWebhookSubscriptionDeletedRevertsToFree(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierPro,
		StripeCustomerID: "cus_123", StripeSubscriptionID: "sub_123", StripeSubscriptionStatus: "active"})
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{"id": "sub_123", "object": "subscription", "customer": "cus_123", "status": "canceled"}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_subdel", "customer.subscription.deleted", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	got, _ := users.GetBillingByID(context.Background(), "user-1")
	if got.Tier != domain.TierFree {
		t.Errorf("tier = %q, want free", got.Tier)
	}
	if got.StripeSubscriptionID != "" || got.StripeSubscriptionStatus != "" || !got.StripeSubscriptionPeriodEnd.IsZero() {
		t.Errorf("subscription state not cleared: %+v", got)
	}
	if !events.has("evt_subdel") {
		t.Error("subscription.deleted event was not recorded")
	}
}

func TestBillingWebhookSubscriptionDeletedUnknownCustomer(t *testing.T) {
	users := newFakeBillingUserStore()
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{"id": "sub_999", "object": "subscription", "customer": "cus_999", "status": "canceled"}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_subdel_ghost", "customer.subscription.deleted", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !events.has("evt_subdel_ghost") {
		t.Error("unknown-customer subscription.deleted was not recorded")
	}
}

// --- Webhook: invoice events ------------------------------------------------------------------

func TestBillingWebhookInvoicePaymentFailedRecordsOnly(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierPro,
		StripeCustomerID: "cus_123", StripeSubscriptionID: "sub_123", StripeSubscriptionStatus: "active"})
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{"id": "in_1", "object": "invoice", "customer": "cus_123"}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_inv_fail", "invoice.payment_failed", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	// Stripe's retry schedule is the mechanism; the tier must be untouched.
	got, _ := users.GetBillingByID(context.Background(), "user-1")
	if got.Tier != domain.TierPro || got.StripeSubscriptionStatus != "active" {
		t.Errorf("state changed on a payment_failed event: %+v", got)
	}
	if !events.has("evt_inv_fail") {
		t.Error("invoice.payment_failed event was not recorded")
	}
}

func TestBillingWebhookInvoicePaymentSucceededRecordsOnly(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{"id": "in_2", "object": "invoice", "customer": "cus_123"}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_inv_ok", "invoice.payment_succeeded", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got, _ := users.GetBillingByID(context.Background(), "user-1"); got.Tier != domain.TierFree {
		t.Errorf("tier = %q, want free (payment_succeeded never changes tier)", got.Tier)
	}
	if !events.has("evt_inv_ok") {
		t.Error("invoice.payment_succeeded event was not recorded")
	}
}

// --- Webhook: unhandled types -----------------------------------------------------------------

func TestBillingWebhookUnknownEventAcknowledged(t *testing.T) {
	users := newFakeBillingUserStore()
	events := newFakeBillingEventStore()
	handler := newBillingTestRouter(users, events, &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	data := map[string]any{"id": "ch_1", "object": "charge", "amount": 2900}
	req := signedWebhookRequest(t, billingEventPayload(t, "evt_charge", "charge.succeeded", data), billingTestSecret)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (unhandled events are acked, not retried forever)", rec.Code, http.StatusOK)
	}
	if !events.has("evt_charge") {
		t.Error("unknown event was not recorded")
	}
}

// --- Subscription read ------------------------------------------------------------------------

func TestBillingSubscriptionReadFree(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodGet, "/v1/billing/subscription", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Subscription publicSubscription `json:"subscription"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Subscription.Plan != "free" || body.Subscription.StripeSubscriptionExists {
		t.Errorf("subscription = %+v, want free + no stripe subscription", body.Subscription)
	}
}

func TestBillingSubscriptionReadActive(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{
		UserID: "user-1", Email: "a@b.com", Tier: domain.TierPro,
		StripeCustomerID: "cus_123", StripeSubscriptionID: "sub_123", StripeSubscriptionStatus: "active",
		StripeSubscriptionPeriodEnd: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
	})
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), &fakeStripeClient{},
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodGet, "/v1/billing/subscription", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		Subscription publicSubscription `json:"subscription"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	s := body.Subscription
	if s.Plan != "pro" || !s.StripeSubscriptionExists || s.StripeSubscriptionStatus != "active" {
		t.Errorf("subscription = %+v", s)
	}
	if s.StripeSubscriptionPeriodEnd != "2026-09-06T12:00:00Z" {
		t.Errorf("period end = %q, want 2026-09-06T12:00:00Z", s.StripeSubscriptionPeriodEnd)
	}
}

func TestBillingPortalReturnsRedirectURL(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{
		UserID: "user-1", Email: "a@b.com", Tier: domain.TierPro,
		StripeCustomerID: "cus_123", StripeSubscriptionID: "sub_123", StripeSubscriptionStatus: "active",
	})
	client := &fakeStripeClient{portalResult: billing.PortalSessionResult{URL: "https://billing.stripe.com/p/session/xyz"}}
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/portal", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	var body struct {
		Portal struct {
			URL string `json:"url"`
		} `json:"portal"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Portal.URL != "https://billing.stripe.com/p/session/xyz" {
		t.Errorf("portal url = %q", body.Portal.URL)
	}
	// The portal session is created for the caller's stored Stripe customer, never a caller-supplied id.
	if customer, ok := client.lastPortalCustomer(); !ok || customer != "cus_123" {
		t.Errorf("portal customer = %q, ok = %v, want cus_123", customer, ok)
	}
}

func TestBillingPortalRejectsAccountWithoutStripeCustomer(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{UserID: "user-1", Email: "a@b.com", Tier: domain.TierFree})
	client := &fakeStripeClient{}
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/portal", nil, "user-1", domain.TierFree)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Code != "no_billing_account" {
		t.Errorf("code = %q, want no_billing_account", body.Code)
	}
	if _, called := client.lastPortalCustomer(); called {
		t.Error("portal session created for an account with no Stripe customer")
	}
}

func TestBillingPortalSurfacesProviderError(t *testing.T) {
	users := newFakeBillingUserStore()
	users.seed(postgres.UserBilling{
		UserID: "user-1", Email: "a@b.com", Tier: domain.TierPro,
		StripeCustomerID: "cus_123",
	})
	client := &fakeStripeClient{portalErr: errors.New("stripe is down")}
	handler := newBillingTestRouter(users, newFakeBillingEventStore(), client,
		billingTestSecret, billingTestPricePro, billingTestPriceTeam, billingTestWebBase)

	req := authedRequest(t, http.MethodPost, "/v1/billing/portal", nil, "user-1", domain.TierPro)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusBadGateway, rec.Body.String())
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if body.Code != "payment_provider_error" {
		t.Errorf("code = %q, want payment_provider_error", body.Code)
	}
}

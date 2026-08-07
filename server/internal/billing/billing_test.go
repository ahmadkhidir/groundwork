package billing

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

const (
	testPricePro  = "price_pro_test"
	testPriceTeam = "price_team_test"
	testSecret    = "whsec_test-secret-for-verification"
)

// signPayload produces the Stripe-Signature header Stripe would send for payload under secret, using
// the SDK's own ComputeSignature so the test exercises the real verification path.
func signPayload(t *testing.T, payload []byte, secret string) string {
	t.Helper()
	ts := time.Now()
	sig := webhook.ComputeSignature(ts, payload, secret)
	return "t=" + strconv.FormatInt(ts.Unix(), 10) + ",v1=" + hex.EncodeToString(sig)
}

// eventPayload builds a Stripe webhook body for the given type and data object.
func eventPayload(t *testing.T, id, eventType string, data any) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"id":          id,
		"object":      "event",
		"api_version": stripe.APIVersion,
		"type":        eventType,
		"data":        map[string]any{"object": data},
	})
	if err != nil {
		t.Fatalf("marshaling event payload: %v", err)
	}
	return payload
}

func TestPriceForPlan(t *testing.T) {
	if got, err := PriceForPlan(testPricePro, testPriceTeam, domain.TierPro); err != nil || got != testPricePro {
		t.Errorf("PriceForPlan(pro) = %q, %v; want %q", got, err, testPricePro)
	}
	if got, err := PriceForPlan(testPricePro, testPriceTeam, domain.TierTeam); err != nil || got != testPriceTeam {
		t.Errorf("PriceForPlan(team) = %q, %v; want %q", got, err, testPriceTeam)
	}
	if _, err := PriceForPlan(testPricePro, testPriceTeam, domain.TierFree); err == nil {
		t.Error("PriceForPlan(free) should error: the free tier has no Stripe price")
	}
	if _, err := PriceForPlan(testPricePro, testPriceTeam, "enterprise"); err == nil {
		t.Error("PriceForPlan(unknown) should error")
	}
	if _, err := PriceForPlan("", testPriceTeam, domain.TierPro); err == nil {
		t.Error("PriceForPlan(pro) with empty pro price should error")
	}
	if _, err := PriceForPlan(testPricePro, "", domain.TierTeam); err == nil {
		t.Error("PriceForPlan(team) with empty team price should error")
	}
}

func TestTierForPrice(t *testing.T) {
	if tier, ok := TierForPrice(testPricePro, testPriceTeam, testPricePro); !ok || tier != domain.TierPro {
		t.Errorf("TierForPrice(pro price) = %q, %v; want pro/true", tier, ok)
	}
	if tier, ok := TierForPrice(testPricePro, testPriceTeam, testPriceTeam); !ok || tier != domain.TierTeam {
		t.Errorf("TierForPrice(team price) = %q, %v; want team/true", tier, ok)
	}
	if _, ok := TierForPrice(testPricePro, testPriceTeam, "price_unknown"); ok {
		t.Error("TierForPrice(unknown price) should be false")
	}
}

func TestVerifyEventAcceptsValidSignatureAndParsesCheckout(t *testing.T) {
	data := map[string]any{
		"id":                  "cs_123",
		"object":              "checkout.session",
		"mode":                "subscription",
		"client_reference_id": "user-1",
		"customer":            "cus_123",
		"subscription":        "sub_123",
		"metadata":            map[string]string{"plan": "pro"},
	}
	payload := eventPayload(t, "evt_checkout", "checkout.session.completed", data)
	header := signPayload(t, payload, testSecret)

	evt, err := VerifyEvent(payload, header, testSecret)
	if err != nil {
		t.Fatalf("VerifyEvent: %v", err)
	}
	if evt.ID != "evt_checkout" || evt.Type != EventCheckoutSessionCompleted {
		t.Fatalf("event identity = %q/%q", evt.ID, evt.Type)
	}
	if evt.Checkout == nil {
		t.Fatal("Checkout payload not parsed")
	}
	c := evt.Checkout
	if c.SessionID != "cs_123" || c.ClientReferenceID != "user-1" || c.CustomerID != "cus_123" ||
		c.SubscriptionID != "sub_123" || c.Plan != "pro" {
		t.Errorf("checkout = %+v, want cs_123/user-1/cus_123/sub_123/pro", c)
	}
}

func TestVerifyEventParsesSubscriptionDeleted(t *testing.T) {
	data := map[string]any{
		"id":       "sub_123",
		"object":   "subscription",
		"customer": "cus_123",
		"status":   "canceled",
	}
	payload := eventPayload(t, "evt_sub_deleted", "customer.subscription.deleted", data)
	header := signPayload(t, payload, testSecret)

	evt, err := VerifyEvent(payload, header, testSecret)
	if err != nil {
		t.Fatalf("VerifyEvent: %v", err)
	}
	if evt.Subscription == nil {
		t.Fatal("Subscription payload not parsed")
	}
	if evt.Subscription.CustomerID != "cus_123" || evt.Subscription.SubscriptionID != "sub_123" || evt.Subscription.Status != "canceled" {
		t.Errorf("subscription = %+v", evt.Subscription)
	}
	if evt.Checkout != nil || evt.Invoice != nil {
		t.Error("unexpected sibling payloads parsed")
	}
}

func TestVerifyEventParsesSubscriptionUpdatedWithPeriodEnd(t *testing.T) {
	data := map[string]any{
		"id":       "sub_123",
		"object":   "subscription",
		"customer": "cus_123",
		"status":   "active",
		"items": map[string]any{
			"object": "list",
			"data": []map[string]any{
				{
					"id":                 "si_1",
					"object":             "subscription_item",
					"price":              map[string]any{"id": testPricePro, "object": "price"},
					"current_period_end": 1757000000,
				},
			},
		},
	}
	payload := eventPayload(t, "evt_sub_updated", "customer.subscription.updated", data)
	header := signPayload(t, payload, testSecret)

	evt, err := VerifyEvent(payload, header, testSecret)
	if err != nil {
		t.Fatalf("VerifyEvent: %v", err)
	}
	if evt.Subscription == nil || evt.Subscription.CurrentPeriodEnd != 1757000000 {
		t.Errorf("subscription = %+v, want period end 1757000000", evt.Subscription)
	}
}

func TestVerifyEventParsesInvoice(t *testing.T) {
	data := map[string]any{
		"id":       "in_1",
		"object":   "invoice",
		"customer": "cus_123",
	}
	payload := eventPayload(t, "evt_inv_failed", "invoice.payment_failed", data)
	header := signPayload(t, payload, testSecret)

	evt, err := VerifyEvent(payload, header, testSecret)
	if err != nil {
		t.Fatalf("VerifyEvent: %v", err)
	}
	if evt.Invoice == nil || evt.Invoice.InvoiceID != "in_1" || evt.Invoice.CustomerID != "cus_123" {
		t.Errorf("invoice = %+v", evt.Invoice)
	}
}

func TestVerifyEventRejectsBadSignature(t *testing.T) {
	payload := eventPayload(t, "evt_x", "checkout.session.completed", map[string]any{"id": "cs_1"})
	header := signPayload(t, payload, testSecret)

	if _, err := VerifyEvent(payload, header, "whsec_wrong-secret"); err == nil {
		t.Fatal("VerifyEvent accepted a signature from the wrong secret")
	}
	if _, err := VerifyEvent(payload, "t=123,v1=deadbeef", testSecret); err == nil {
		t.Fatal("VerifyEvent accepted a garbage signature")
	}
	if _, err := VerifyEvent(payload, "", testSecret); err == nil {
		t.Fatal("VerifyEvent accepted a missing signature header")
	}
}

func TestVerifyEventUnknownTypeParsesWithoutPayload(t *testing.T) {
	payload := eventPayload(t, "evt_unknown", "charge.succeeded", map[string]any{"id": "ch_1"})
	header := signPayload(t, payload, testSecret)

	evt, err := VerifyEvent(payload, header, testSecret)
	if err != nil {
		t.Fatalf("VerifyEvent: %v", err)
	}
	if evt.Type != "charge.succeeded" {
		t.Errorf("type = %q, want charge.succeeded", evt.Type)
	}
	if evt.Checkout != nil || evt.Subscription != nil || evt.Invoice != nil {
		t.Error("unknown event type must not populate typed payloads")
	}
}

func TestBuildCheckoutSessionParams(t *testing.T) {
	params := buildCheckoutSessionParams(CheckoutParams{
		UserID:     "user-1",
		Email:      "a@b.com",
		Plan:       domain.TierPro,
		PriceID:    testPricePro,
		SuccessURL: "http://localhost:3000/settings?upgrade=success",
		CancelURL:  "http://localhost:3000/settings?upgrade=cancelled",
	})

	if params.Mode == nil || *params.Mode != string(stripe.CheckoutSessionModeSubscription) {
		t.Errorf("mode = %v, want subscription", params.Mode)
	}
	if params.ClientReferenceID == nil || *params.ClientReferenceID != "user-1" {
		t.Errorf("client_reference_id = %v, want user-1", params.ClientReferenceID)
	}
	if params.CustomerEmail == nil || *params.CustomerEmail != "a@b.com" {
		t.Errorf("customer_email = %v, want a@b.com", params.CustomerEmail)
	}
	if params.Metadata[PlanMetadataKey] != "pro" {
		t.Errorf("metadata plan = %q, want pro", params.Metadata[PlanMetadataKey])
	}
	if params.SuccessURL == nil || *params.SuccessURL != "http://localhost:3000/settings?upgrade=success" {
		t.Errorf("success_url = %v", params.SuccessURL)
	}
	if params.CancelURL == nil || *params.CancelURL != "http://localhost:3000/settings?upgrade=cancelled" {
		t.Errorf("cancel_url = %v", params.CancelURL)
	}
	if len(params.LineItems) != 1 || params.LineItems[0].Price == nil || *params.LineItems[0].Price != testPricePro {
		t.Errorf("line_items = %+v, want one line item priced at %s", params.LineItems, testPricePro)
	}
	if params.LineItems[0].Quantity == nil || *params.LineItems[0].Quantity != 1 {
		t.Errorf("quantity = %v, want 1", params.LineItems[0].Quantity)
	}
}

func TestSubscriptionSnapshotFromStripe(t *testing.T) {
	sub := &stripe.Subscription{
		ID:     "sub_123",
		Status: stripe.SubscriptionStatusActive,
		Items: &stripe.SubscriptionItemList{
			Data: []*stripe.SubscriptionItem{
				{
					ID:               "si_1",
					CurrentPeriodEnd: 1757000000,
					Price:            &stripe.Price{ID: testPricePro},
				},
			},
		},
	}
	snap := subscriptionSnapshotFromStripe(sub)
	if snap.ID != "sub_123" || snap.Status != "active" || snap.CurrentPeriodEnd != 1757000000 || snap.PriceID != testPricePro {
		t.Errorf("snapshot = %+v", snap)
	}

	empty := subscriptionSnapshotFromStripe(&stripe.Subscription{ID: "sub_2", Status: stripe.SubscriptionStatusCanceled})
	if empty.Status != "canceled" || empty.CurrentPeriodEnd != 0 || empty.PriceID != "" {
		t.Errorf("empty snapshot = %+v", empty)
	}
}

func TestClientNewDoesNotPanic(t *testing.T) {
	c := NewClient("sk_test_dummy")
	if c == nil || c.stripe == nil {
		t.Fatal("NewClient returned a nil client")
	}
	// Constructing the request params must not require a network call; this pins the SDK surface.
	params := buildCheckoutSessionParams(CheckoutParams{
		UserID: "u", Email: "e", Plan: domain.TierPro, PriceID: testPricePro,
		SuccessURL: "http://x/s", CancelURL: "http://x/c",
	})
	if params.LineItems == nil || len(params.LineItems) != 1 || params.LineItems[0].Price == nil {
		t.Fatal("params did not build a line item with a price")
	}
}

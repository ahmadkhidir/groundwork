// Package billing is the Stripe integration seam (Billing milestone, pass 2): the pure plan↔price
// mapping, the Stripe API wrapper, and webhook signature verification + event parsing.
//
// Layering: this package is a leaf — it imports the Stripe SDK, the standard library, and
// internal/domain's tier enum, and nothing else from this codebase. The HTTP handlers live in
// internal/api (billing.go) and depend on this package plus narrow persistence seams; the concrete
// stores live in internal/store/postgres. Webhook verification is a pure function (no network), so
// the handler and tests exercise it directly; the two network calls (CreateCheckoutSession,
// GetSubscription) are thin wrappers over pure helpers (buildCheckoutSessionParams,
// subscriptionSnapshotFromStripe) so the request/response shaping is testable without a Stripe call.
//
// Security: the webhook route is PUBLIC (Stripe has no Auth.js session cookie) — the
// Stripe-Signature header verified here is the authentication. The checkout and subscription-read
// routes sit behind authsession.RequireAuth in api.go. The API key and webhook secret come from the
// environment (server/internal/config, rule 12); this package never reads them itself.
//
// Monetization mapping: monetization.md's catalog is Free (no price) / Pro / Team. Checkout is only
// ever created for the two paid plans; "free" has no Stripe Price and is never sent to Stripe.
package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/stripe/stripe-go/v86"
	"github.com/stripe/stripe-go/v86/webhook"

	"github.com/ahmadkhidir/groundwork/server/internal/domain"
)

// PlanMetadataKey is the Checkout Session metadata key carrying the plan ("pro" or "team"). It is
// written at session creation and read back by the webhook — the only place the webhook learns which
// tier a completed checkout paid for without an extra API call. Metadata is set by our own code with
// our API key, so it is trustworthy; the subscription snapshot's price id is cross-checked against it.
const PlanMetadataKey = "plan"

// PriceForPlan returns the Stripe Price ID for a paid plan, or an error for free (which has no price)
// and for any unrecognized tier. pricePro/priceTeam are the configured catalog Price IDs.
func PriceForPlan(pricePro, priceTeam string, plan domain.SubscriptionTier) (string, error) {
	switch plan {
	case domain.TierPro:
		if pricePro == "" {
			return "", errors.New("billing: STRIPE_PRICE_PRO is not configured")
		}
		return pricePro, nil
	case domain.TierTeam:
		if priceTeam == "" {
			return "", errors.New("billing: STRIPE_PRICE_TEAM is not configured")
		}
		return priceTeam, nil
	case domain.TierFree:
		return "", errors.New("billing: the free tier has no Stripe price")
	default:
		return "", fmt.Errorf("billing: unknown plan %q", plan)
	}
}

// TierForPrice returns the tier a Stripe Price ID maps to, or false if it is not one of the
// configured catalog prices. This is the price_id → tier direction the webhook needs when it
// cross-checks a subscription's price against the plan recorded in Checkout metadata.
func TierForPrice(pricePro, priceTeam, priceID string) (domain.SubscriptionTier, bool) {
	switch priceID {
	case pricePro:
		return domain.TierPro, true
	case priceTeam:
		return domain.TierTeam, true
	default:
		return "", false
	}
}

// EventType names the webhook event types this integration acts on. Other Stripe events are
// acknowledged (recorded) and ignored.
type EventType string

// Webhook event types handled by the integration, mirroring stripe.EventType values.
const (
	EventCheckoutSessionCompleted    EventType = "checkout.session.completed"
	EventCustomerSubscriptionUpdated EventType = "customer.subscription.updated"
	EventCustomerSubscriptionDeleted EventType = "customer.subscription.deleted"
	EventInvoicePaymentFailed        EventType = "invoice.payment_failed"
	EventInvoicePaymentSucceeded     EventType = "invoice.payment_succeeded"
)

// CheckoutCompleted is the parsed payload of a checkout.session.completed event.
type CheckoutCompleted struct {
	SessionID         string
	ClientReferenceID string // the user id we set at session creation
	CustomerID        string
	SubscriptionID    string
	Plan              string // metadata["plan"]; empty if metadata was not set (a bug to surface)
}

// SubscriptionChanged is the parsed payload of a customer.subscription.updated / .deleted event.
type SubscriptionChanged struct {
	SubscriptionID   string
	CustomerID       string
	Status           string
	CurrentPeriodEnd int64 // unix seconds; 0 when absent (e.g. deleted events)
}

// InvoiceEvent is the parsed payload of an invoice.payment_failed / payment_succeeded event.
// SubscriptionID is intentionally absent: the v86 SDK's Invoice object exposes the subscription only
// through Parent.SubscriptionDetails (an expanded-parent detail), and invoice events are log-only in
// this pass — the customer id is enough to attribute the record.
type InvoiceEvent struct {
	InvoiceID  string
	CustomerID string
}

// Event is the parsed, verified webhook event handed to the HTTP handler. Exactly one payload
// field is set, depending on Type.
type Event struct {
	ID   string
	Type EventType
	// Checkout is set when Type == EventCheckoutSessionCompleted.
	Checkout *CheckoutCompleted
	// Subscription is set when Type is EventCustomerSubscriptionUpdated or EventCustomerSubscriptionDeleted.
	Subscription *SubscriptionChanged
	// Invoice is set when Type is EventInvoicePaymentFailed or EventInvoicePaymentSucceeded.
	Invoice *InvoiceEvent
}

// CheckoutParams is what the checkout handler needs to create a Checkout Session.
type CheckoutParams struct {
	// UserID rides as client_reference_id — the account identity Stripe returns on the webhook.
	UserID string
	// Email prefills the customer's email in the Stripe-hosted page and anchors the created Customer.
	Email string
	// Plan is the tier being purchased (TierPro or TierTeam), recorded in session metadata.
	Plan domain.SubscriptionTier
	// PriceID is the recurring Price for Plan, resolved via PriceForPlan by the handler.
	PriceID string
	// SuccessURL / CancelURL are where Stripe redirects the browser after checkout.
	SuccessURL string
	CancelURL  string
}

// CheckoutResult is what CreateCheckoutSession returns.
type CheckoutResult struct {
	SessionID string
	URL       string
}

// SubscriptionSnapshot is the subset of a Stripe Subscription the webhook stores.
type SubscriptionSnapshot struct {
	ID               string
	Status           string
	CurrentPeriodEnd int64 // unix seconds; 0 when absent
	PriceID          string
}

// PortalSessionResult is what CreatePortalSession returns.
type PortalSessionResult struct {
	// URL is the Stripe Customer Portal URL the browser should be handed to.
	URL string
}

// Client wraps the Stripe SDK's typed services. It holds the API key on the instance (via
// stripe.NewClient) and never mutates the package-level stripe.Key global — constructor injection,
// matching the codebase's no-global-mutable-state rule.
type Client struct {
	stripe *stripe.Client
}

// NewClient constructs a Client for the given secret key. key is the test-mode or live secret key
// from the environment (STRIPE_SECRET_KEY); it is never logged or echoed.
func NewClient(key string) *Client {
	return &Client{stripe: stripe.NewClient(key)}
}

// CreateCheckoutSession creates a subscription-mode Checkout Session for the user and returns the
// redirect URL. It is the ONLY path that sends a user to Stripe; the webhook (not this call) applies
// the tier change once payment completes.
func (c *Client) CreateCheckoutSession(ctx context.Context, p CheckoutParams) (CheckoutResult, error) {
	params := buildCheckoutSessionParams(p)
	// The SDK copies ctx into params.Context itself, but setting it here keeps the pure helper
	// free of any SDK mutation semantics and makes the call explicit.
	params.Context = ctx
	session, err := c.stripe.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("billing: creating checkout session: %w", err)
	}
	return CheckoutResult{SessionID: session.ID, URL: session.URL}, nil
}

// GetSubscription retrieves a subscription's current status, period end, and price id — the data
// the webhook stores for the subscription read endpoint. Called on checkout.session.completed so the
// stored state is complete immediately (Stripe's later subscription.updated events keep it fresh).
func (c *Client) GetSubscription(ctx context.Context, id string) (SubscriptionSnapshot, error) {
	sub, err := c.stripe.V1Subscriptions.Retrieve(ctx, id, &stripe.SubscriptionRetrieveParams{
		Params: stripe.Params{Context: ctx},
	})
	if err != nil {
		return SubscriptionSnapshot{}, fmt.Errorf("billing: retrieving subscription %s: %w", id, err)
	}
	return subscriptionSnapshotFromStripe(sub), nil
}

// CreatePortalSession creates a Stripe Customer Portal session for customerID — the self-serve
// surface where a user cancels or changes their plan (monetization.md's Cancellation / Downgrade
// events). The existing webhooks (customer.subscription.deleted, customer.subscription.updated)
// keep stored tier/subscription state fresh after the portal does its work, so this endpoint only
// ever hands back a redirect URL.
func (c *Client) CreatePortalSession(ctx context.Context, customerID string) (PortalSessionResult, error) {
	if customerID == "" {
		// A caller with no stored customer cannot have a subscription to manage; surfacing this as
		// an error keeps the handler honest instead of sending Stripe an empty customer id.
		return PortalSessionResult{}, errors.New("billing: no Stripe customer for the portal session")
	}
	params := &stripe.BillingPortalSessionCreateParams{
		Customer: stripe.String(customerID),
	}
	params.Context = ctx
	session, err := c.stripe.V1BillingPortalSessions.Create(ctx, params)
	if err != nil {
		return PortalSessionResult{}, fmt.Errorf("billing: creating billing portal session: %w", err)
	}
	return PortalSessionResult{URL: session.URL}, nil
}

// CancelSubscription cancels a subscription IMMEDIATELY — FEAT-052's account deletion cancels at
// once, deliberately different from plan cancellation's end-of-period semantics (monetization.md).
// The customer keeps paid access until Stripe processes the cancellation; a cancellation_failed
// failure aborts the account cascade so an account that "deleted" but kept paying Stripe is never
// a false success.
func (c *Client) CancelSubscription(ctx context.Context, subscriptionID string) error {
	params := &stripe.SubscriptionCancelParams{}
	params.Context = ctx
	if _, err := c.stripe.V1Subscriptions.Cancel(ctx, subscriptionID, params); err != nil {
		return fmt.Errorf("billing: cancelling subscription: %w", err)
	}
	return nil
}

// DeleteCustomer deletes a Stripe Customer object — FEAT-052's account deletion removes the
// customer (and with it Stripe-side payment methods and customer PII) after cancelling any active
// subscription. Historical invoices/charges Stripe retains per its own policy; that is outside
// Groundwork's control and documented as such.
func (c *Client) DeleteCustomer(ctx context.Context, customerID string) error {
	params := &stripe.CustomerDeleteParams{}
	params.Context = ctx
	if _, err := c.stripe.V1Customers.Delete(ctx, customerID, params); err != nil {
		return fmt.Errorf("billing: deleting stripe customer: %w", err)
	}
	return nil
}

// buildCheckoutSessionParams is the pure constructor for the Stripe request — extracted so the
// request shape (price, client_reference_id, metadata, URLs, mode) is pinned by tests without a
// network call.
func buildCheckoutSessionParams(p CheckoutParams) *stripe.CheckoutSessionCreateParams {
	return &stripe.CheckoutSessionCreateParams{
		Mode:              stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		ClientReferenceID: stripe.String(p.UserID),
		CustomerEmail:     stripe.String(p.Email),
		SuccessURL:        stripe.String(p.SuccessURL),
		CancelURL:         stripe.String(p.CancelURL),
		Metadata:          map[string]string{PlanMetadataKey: string(p.Plan)},
		LineItems: []*stripe.CheckoutSessionCreateLineItemParams{
			{
				Price:    stripe.String(p.PriceID),
				Quantity: stripe.Int64(1),
			},
		},
	}
}

// subscriptionSnapshotFromStripe is the pure extractor for the subset of a Stripe Subscription the
// integration stores — status, current period end, and the first item's price id. A subscription
// always has exactly one item in this integration (single price per checkout).
func subscriptionSnapshotFromStripe(sub *stripe.Subscription) SubscriptionSnapshot {
	snap := SubscriptionSnapshot{ID: sub.ID, Status: string(sub.Status)}
	if sub.Items != nil && len(sub.Items.Data) > 0 {
		item := sub.Items.Data[0]
		snap.CurrentPeriodEnd = item.CurrentPeriodEnd
		if item.Price != nil {
			snap.PriceID = item.Price.ID
		}
	}
	return snap
}

// VerifyEvent verifies the Stripe-Signature header over payload with secret and parses the event
// into the typed payloads the handler switches on. It is the webhook route's authentication: a
// request that fails here is rejected before any state is touched.
//
// The SDK's api-version compatibility check is deliberately disabled (IgnoreAPIVersionMismatch):
// signature verification is the security boundary, and a dev `stripe listen`/dashboard endpoint
// created at a slightly different API version would otherwise fail every webhook. Object fields this
// integration reads (client_reference_id, metadata, subscription status/period end, invoice ids) are
// stable across recent API versions.
func VerifyEvent(payload []byte, sigHeader, secret string) (Event, error) {
	event, err := webhook.ConstructEventWithOptions(payload, sigHeader, secret, webhook.ConstructEventOptions{
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return Event{}, fmt.Errorf("billing: verifying webhook signature: %w", err)
	}

	out := Event{ID: event.ID, Type: EventType(event.Type)}
	if event.Data == nil {
		return out, nil
	}

	switch EventType(event.Type) {
	case EventCheckoutSessionCompleted:
		var cs stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &cs); err != nil {
			return Event{}, fmt.Errorf("billing: parsing checkout session event: %w", err)
		}
		out.Checkout = &CheckoutCompleted{
			SessionID:         cs.ID,
			ClientReferenceID: cs.ClientReferenceID,
			CustomerID:        customerID(cs.Customer),
			SubscriptionID:    subscriptionID(cs.Subscription),
			Plan:              cs.Metadata[PlanMetadataKey],
		}
	case EventCustomerSubscriptionUpdated, EventCustomerSubscriptionDeleted:
		var sub stripe.Subscription
		if err := json.Unmarshal(event.Data.Raw, &sub); err != nil {
			return Event{}, fmt.Errorf("billing: parsing subscription event: %w", err)
		}
		snap := subscriptionSnapshotFromStripe(&sub)
		out.Subscription = &SubscriptionChanged{
			SubscriptionID:   snap.ID,
			CustomerID:       customerID(sub.Customer),
			Status:           snap.Status,
			CurrentPeriodEnd: snap.CurrentPeriodEnd,
		}
	case EventInvoicePaymentFailed, EventInvoicePaymentSucceeded:
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
			return Event{}, fmt.Errorf("billing: parsing invoice event: %w", err)
		}
		out.Invoice = &InvoiceEvent{
			InvoiceID:  inv.ID,
			CustomerID: customerID(inv.Customer),
		}
	}
	return out, nil
}

// customerID unwraps an expandable customer reference (either an id string or an expanded object).
func customerID(c *stripe.Customer) string {
	if c == nil {
		return ""
	}
	return c.ID
}

// subscriptionID unwraps an expandable subscription reference (either an id string or an expanded
// object). invoice.Subscription and checkout session.Subscription are both of this shape.
func subscriptionID(s *stripe.Subscription) string {
	if s == nil {
		return ""
	}
	return s.ID
}

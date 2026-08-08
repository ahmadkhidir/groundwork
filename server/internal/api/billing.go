package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ahmadkhidir/groundwork/server/internal/auth"
	"github.com/ahmadkhidir/groundwork/server/internal/billing"
	"github.com/ahmadkhidir/groundwork/server/internal/domain"
	"github.com/ahmadkhidir/groundwork/server/internal/store/postgres"
)

// upgradeSuccessPath and upgradeCancelledPath are where Stripe redirects the browser after checkout
// completes or is cancelled. Pass 3's billing UI owns the /settings route and reads the `upgrade`
// query parameter to show the outcome; these paths are the contract between the webhook-side wiring
// here and that screen. They are reported in the pass-2 report so Pass 3 matches them exactly.
const (
	upgradeSuccessPath   = "/settings?upgrade=success"
	upgradeCancelledPath = "/settings?upgrade=cancelled"
)

// BillingUserStore is the persistence seam the billing handlers depend on, satisfied by
// internal/store/postgres.UserRepository. It is the ONLY seam that may change a user's tier —
// the billing webhook path owns tier transitions per monetization.md's Billing Events; no feature
// slice calls these methods.
type BillingUserStore interface {
	GetBillingByID(ctx context.Context, userID string) (postgres.UserBilling, error)
	GetBillingByStripeCustomerID(ctx context.Context, customerID string) (postgres.UserBilling, error)
	UpdateTier(ctx context.Context, userID string, tier domain.SubscriptionTier) error
	SetStripeCustomer(ctx context.Context, userID, customerID string) error
	SetStripeSubscription(ctx context.Context, userID, subscriptionID, status string, periodEnd time.Time) error
}

// BillingEventStore is the webhook idempotency/event-record seam, satisfied by
// postgres.BillingEventRepository. RecordIfNew inserts the event only if its Stripe event id has
// never been recorded; the handler treats a duplicate as an acknowledged replay (at-least-once).
type BillingEventStore interface {
	RecordIfNew(ctx context.Context, eventID, eventType, userID string, data []byte) (bool, error)
}

// StripeClient is the Stripe API seam, satisfied by *billing.Client. Narrow by construction: the
// handlers only create checkout sessions, read one subscription snapshot, and open the customer
// portal.
type StripeClient interface {
	CreateCheckoutSession(ctx context.Context, p billing.CheckoutParams) (billing.CheckoutResult, error)
	GetSubscription(ctx context.Context, id string) (billing.SubscriptionSnapshot, error)
	CreatePortalSession(ctx context.Context, customerID string) (billing.PortalSessionResult, error)
}

// RegisterBillingRoutes wires the AUTHENTICATED billing endpoints onto r (mounted inside
// authsession.RequireAuth by the caller — see api.go): the subscription read Pass 3's plan screen
// needs, and the checkout creation the upgrade flow needs. pricePro/priceTeam are the catalog Price
// IDs (config STRIPE_PRICE_PRO / STRIPE_PRICE_TEAM); webBaseURL is the web origin the success/cancel
// redirect URLs are built from (config AllowedOrigin — the CORS origin IS the web origin).
func RegisterBillingRoutes(r chi.Router, users BillingUserStore, client StripeClient, pricePro, priceTeam, webBaseURL string, logger *slog.Logger) {
	r.Get("/billing/subscription", handleGetSubscription(users, logger))
	r.Post("/billing/checkout", handleCreateCheckoutSession(users, client, pricePro, priceTeam, webBaseURL, logger))
	r.Post("/billing/portal", handleCreatePortalSession(users, client, logger))
}

// RegisterStripeWebhookRoute wires the PUBLIC Stripe webhook onto r (registered on the /v1 group
// OUTSIDE authsession.RequireAuth — Stripe has no Auth.js cookie; the Stripe-Signature header
// verified in the handler is the authentication). Stripe must always be able to reach this route.
func RegisterStripeWebhookRoute(r chi.Router, users BillingUserStore, events BillingEventStore, client StripeClient, webhookSecret, pricePro, priceTeam string, logger *slog.Logger) {
	r.Post("/billing/webhook", handleStripeWebhook(users, events, client, webhookSecret, pricePro, priceTeam, logger))
}

// publicSubscription is GET /v1/billing/subscription's payload — the shape Pass 3's plan screen
// renders. Plan is the authoritative users.tier value; the Stripe fields describe the stored
// subscription state (empty when none exists). Status is Stripe's own value, surfaced verbatim.
type publicSubscription struct {
	Plan                        string `json:"plan"`
	StripeSubscriptionExists    bool   `json:"stripe_subscription_exists"`
	StripeSubscriptionStatus    string `json:"stripe_subscription_status,omitempty"`
	StripeSubscriptionPeriodEnd string `json:"stripe_subscription_period_end,omitempty"`
}

func toPublicSubscription(info postgres.UserBilling) publicSubscription {
	out := publicSubscription{
		Plan:                     string(info.Tier),
		StripeSubscriptionExists: info.StripeSubscriptionID != "",
		StripeSubscriptionStatus: info.StripeSubscriptionStatus,
	}
	if !info.StripeSubscriptionPeriodEnd.IsZero() {
		out.StripeSubscriptionPeriodEnd = info.StripeSubscriptionPeriodEnd.UTC().Format(time.RFC3339)
	}
	return out
}

// handleGetSubscription serves GET /v1/billing/subscription: the caller's current plan and the
// stored Stripe subscription state (exists / status / period end). Subscription state is STORED on
// the user row by the webhooks rather than queried live from Stripe per request — the read endpoint
// stays fast and offline-tolerant, and the webhook handlers are the single writer.
func handleGetSubscription(users BillingUserStore, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		info, err := users.GetBillingByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user billing state", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{"subscription": toPublicSubscription(info)})
	}
}

// portalResult is POST /v1/billing/portal's payload — the Customer Portal URL the browser should
// be handed to.
type portalResult struct {
	URL string `json:"url"`
}

// handleCreatePortalSession serves POST /v1/billing/portal: creates a Stripe Customer Portal session
// for the caller's Stripe customer and returns the redirect URL.
//
// The portal is where self-serve cancellation and plan changes happen (monetization.md's
// Cancellation / Downgrade events); the existing webhooks — customer.subscription.deleted (revert to
// Free at period end) and customer.subscription.updated (status/period end) — keep stored
// tier/subscription state fresh after the portal does its work. This handler never changes a tier
// itself; it only opens the door.
func handleCreatePortalSession(users BillingUserStore, client StripeClient, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		info, err := users.GetBillingByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user billing state for portal", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}
		if info.StripeCustomerID == "" {
			// No Stripe customer means no subscription — there is nothing to manage in the portal.
			// An honest 409 the UI can branch on, never a fabricated portal session.
			respondJSON(w, http.StatusConflict, map[string]string{
				"error": "No active billing account to manage.",
				"code":  "no_billing_account",
			})
			return
		}

		result, err := client.CreatePortalSession(r.Context(), info.StripeCustomerID)
		if err != nil {
			logger.Error("creating billing portal session", slog.String("error", err.Error()))
			respondJSON(w, http.StatusBadGateway, map[string]string{
				"error": "The payment provider could not open billing management right now.",
				"code":  "payment_provider_error",
			})
			return
		}

		respondJSON(w, http.StatusCreated, map[string]any{"portal": portalResult{URL: result.URL}})
	}
}

// createCheckoutRequest is the POST body for the upgrade flow: the plan being purchased.
type createCheckoutRequest struct {
	Plan string `json:"plan"`
}

// handleCreateCheckoutSession serves POST /v1/billing/checkout: creates a subscription-mode Stripe
// Checkout Session for the caller and returns the redirect URL. It never applies a tier change
// itself — that happens in the webhook when payment completes (monetization.md's Upgrade event:
// immediate access, Stripe prorates).
func handleCreateCheckoutSession(users BillingUserStore, client StripeClient, pricePro, priceTeam, webBaseURL string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		claims, ok := requireClaims(w, r)
		if !ok {
			return
		}

		var req createCheckoutRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		plan := domain.SubscriptionTier(strings.ToLower(strings.TrimSpace(req.Plan)))
		if plan != domain.TierPro && plan != domain.TierTeam {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Choose a plan to upgrade to (pro or team).",
				"code":  "validation_error",
			})
			return
		}

		priceID, err := billing.PriceForPlan(pricePro, priceTeam, plan)
		if err != nil {
			// A configuration gap, not a client error: the catalog price for a purchasable plan is
			// missing from the environment. Honest 500 naming the gap, never a fabricated price.
			logger.Error("billing price not configured", slog.String("plan", string(plan)))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Billing is not configured for that plan yet.",
				"code":  "billing_not_configured",
			})
			return
		}

		info, err := users.GetBillingByID(r.Context(), claims.UserID)
		if err != nil {
			logger.Error("looking up user for checkout", slog.String("error", err.Error()))
			respondJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "Something went wrong. Try again.",
				"code":  "internal_error",
			})
			return
		}

		result, err := client.CreateCheckoutSession(r.Context(), billing.CheckoutParams{
			UserID:     claims.UserID,
			Email:      info.Email,
			Plan:       plan,
			PriceID:    priceID,
			SuccessURL: webBaseURL + upgradeSuccessPath,
			CancelURL:  webBaseURL + upgradeCancelledPath,
		})
		if err != nil {
			logger.Error("creating checkout session", slog.String("error", err.Error()))
			respondJSON(w, http.StatusBadGateway, map[string]string{
				"error": "The payment provider could not start checkout right now.",
				"code":  "payment_provider_error",
			})
			return
		}

		respondJSON(w, http.StatusCreated, map[string]any{"checkout": map[string]string{
			"session_id": result.SessionID,
			"url":        result.URL,
		}})
	}
}

// handleStripeWebhook serves POST /v1/billing/webhook — the route Stripe calls with billing events.
//
// Authentication is the Stripe-Signature header, verified by billing.VerifyEvent before any state
// is touched; a request that fails verification is rejected with 400 and never reaches the stores.
//
// Idempotency: every event is processed (all writes are idempotent) and THEN recorded in
// billing_events via RecordIfNew — the row is a success marker, not a claim, so a crash between
// apply and record leaves nothing recorded and Stripe's retry replays the event safely; a duplicate
// delivery finds the event already recorded and is acknowledged without double-applying (the writes
// being idempotent is the second layer). A processing error returns 500 WITHOUT recording so Stripe
// retries and the event is not silently lost.
func handleStripeWebhook(users BillingUserStore, events BillingEventStore, client StripeClient, webhookSecret, pricePro, priceTeam string, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Stripe payloads are small; the bound is defense against a misdirected or malicious body.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Could not read the webhook payload.",
				"code":  "invalid_payload",
			})
			return
		}

		evt, err := billing.VerifyEvent(payload, r.Header.Get("Stripe-Signature"), webhookSecret)
		if err != nil {
			logger.Warn("rejecting unverifiable Stripe webhook",
				slog.Bool("has_signature", r.Header.Get("Stripe-Signature") != ""),
				slog.String("error", err.Error()))
			respondJSON(w, http.StatusBadRequest, map[string]string{
				"error": "Invalid webhook signature.",
				"code":  "invalid_signature",
			})
			return
		}

		switch evt.Type {
		case billing.EventCheckoutSessionCompleted:
			if err := applyCheckoutCompleted(r.Context(), users, client, events, evt, pricePro, priceTeam, logger); err != nil {
				logger.Error("applying checkout.session.completed", slog.String("event_id", evt.ID), slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Webhook processing failed.", "code": "webhook_processing_failed"})
				return
			}
		case billing.EventCustomerSubscriptionDeleted:
			if err := applySubscriptionDeleted(r.Context(), users, events, evt); err != nil {
				logger.Error("applying customer.subscription.deleted", slog.String("event_id", evt.ID), slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Webhook processing failed.", "code": "webhook_processing_failed"})
				return
			}
		case billing.EventCustomerSubscriptionUpdated:
			if err := applySubscriptionUpdated(r.Context(), users, events, evt); err != nil {
				logger.Error("applying customer.subscription.updated", slog.String("event_id", evt.ID), slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Webhook processing failed.", "code": "webhook_processing_failed"})
				return
			}
		case billing.EventInvoicePaymentFailed, billing.EventInvoicePaymentSucceeded:
			// Stripe's own retry schedule is the mechanism; the event is recorded, never acted on.
			if err := recordInvoiceEvent(r.Context(), users, events, evt); err != nil {
				logger.Error("recording invoice event", slog.String("event_id", evt.ID), slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Webhook processing failed.", "code": "webhook_processing_failed"})
				return
			}
		default:
			// Unhandled event types are acknowledged (recorded) rather than 500'd — Stripe sends many
			// types this integration deliberately ignores.
			if err := recordEvent(r.Context(), events, evt, "", map[string]any{"event_type": string(evt.Type)}); err != nil {
				logger.Error("recording unhandled webhook event", slog.String("event_id", evt.ID), slog.String("error", err.Error()))
				respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "Webhook processing failed.", "code": "webhook_processing_failed"})
				return
			}
		}

		respondJSON(w, http.StatusOK, map[string]any{"received": true, "event_id": evt.ID})
	}
}

// applyCheckoutCompleted applies monetization.md's Upgrade event: the user's tier is set to the plan
// from the Checkout Session metadata immediately (Stripe handles proration), and the Stripe customer
// and subscription state are stored so the subscription read endpoint and later events have them.
func applyCheckoutCompleted(ctx context.Context, users BillingUserStore, client StripeClient, events BillingEventStore, evt billing.Event, pricePro, priceTeam string, logger *slog.Logger) error {
	checkout := evt.Checkout
	if checkout == nil {
		return errors.New("billing: checkout.session.completed without parsed payload")
	}
	if checkout.ClientReferenceID == "" {
		return errors.New("billing: checkout session carried no client_reference_id")
	}
	if checkout.Plan == "" {
		return errors.New("billing: checkout session carried no plan metadata")
	}
	plan := domain.SubscriptionTier(checkout.Plan)
	if plan != domain.TierPro && plan != domain.TierTeam {
		return errors.New("billing: checkout session carried an unknown plan")
	}

	user, err := users.GetBillingByID(ctx, checkout.ClientReferenceID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			// The account is gone but the charge happened. Record the event and ack so Stripe stops
			// retrying — the event row is the audit trail for a manual refund; a webhook that 500s
			// forever is worse than a recorded gap.
			return recordEvent(ctx, events, evt, "", map[string]any{
				"event_type":      string(evt.Type),
				"plan":            string(plan),
				"customer_id":     checkout.CustomerID,
				"subscription_id": checkout.SubscriptionID,
				"note":            "user not found",
			})
		}
		return err
	}

	if checkout.CustomerID != "" {
		if err := users.SetStripeCustomer(ctx, user.UserID, checkout.CustomerID); err != nil {
			return err
		}
	}

	// Best-effort subscription snapshot: if Stripe's subscription is momentarily unreachable, the
	// tier is still set from the metadata plan and a later customer.subscription.updated event fills
	// the status and period end.
	snapshot := billing.SubscriptionSnapshot{}
	if checkout.SubscriptionID != "" {
		snap, err := client.GetSubscription(ctx, checkout.SubscriptionID)
		if err != nil {
			logger.Warn("billing: fetching subscription after checkout",
				slog.String("subscription_id", checkout.SubscriptionID), slog.String("error", err.Error()))
		} else {
			snapshot = snap
			if snap.PriceID != "" {
				if got, ok := billing.TierForPrice(pricePro, priceTeam, snap.PriceID); !ok {
					logger.Warn("billing: subscription price is not in the catalog",
						slog.String("subscription_id", checkout.SubscriptionID), slog.String("price_id", snap.PriceID))
				} else if got != plan {
					logger.Warn("billing: checkout metadata plan and subscription price disagree",
						slog.String("metadata_plan", string(plan)), slog.String("price_plan", string(got)))
				}
			}
		}
	}

	var periodEnd time.Time
	if snapshot.CurrentPeriodEnd > 0 {
		periodEnd = time.Unix(snapshot.CurrentPeriodEnd, 0).UTC()
	}
	if err := users.SetStripeSubscription(ctx, user.UserID, checkout.SubscriptionID, snapshot.Status, periodEnd); err != nil {
		return err
	}
	if err := users.UpdateTier(ctx, user.UserID, plan); err != nil {
		return err
	}

	return recordEvent(ctx, events, evt, user.UserID, map[string]any{
		"event_type":      string(evt.Type),
		"plan":            string(plan),
		"tier_from":       string(user.Tier),
		"tier_to":         string(plan),
		"customer_id":     checkout.CustomerID,
		"subscription_id": checkout.SubscriptionID,
		"status":          snapshot.Status,
	})
}

// applySubscriptionDeleted applies monetization.md's Cancellation event (end-of-period cancellation
// AND final failed payment arrive here): paid access is over, the account reverts to Free tier
// limits. No data is deleted — the one-workspace-one-delete cascade is a distinct, explicit user
// action, never an automatic consequence of billing (the excess-workspace read-only gate handles the
// workspace-count consequence at the access layer, not here).
func applySubscriptionDeleted(ctx context.Context, users BillingUserStore, events BillingEventStore, evt billing.Event) error {
	sub := evt.Subscription
	if sub == nil {
		return errors.New("billing: subscription.deleted without parsed payload")
	}
	if sub.CustomerID == "" {
		return errors.New("billing: subscription.deleted without a customer id")
	}

	user, err := users.GetBillingByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return recordEvent(ctx, events, evt, "", map[string]any{
				"event_type":      string(evt.Type),
				"customer_id":     sub.CustomerID,
				"subscription_id": sub.SubscriptionID,
				"note":            "customer not found",
			})
		}
		return err
	}

	if err := users.SetStripeSubscription(ctx, user.UserID, "", "", time.Time{}); err != nil {
		return err
	}
	if err := users.UpdateTier(ctx, user.UserID, domain.TierFree); err != nil {
		return err
	}

	return recordEvent(ctx, events, evt, user.UserID, map[string]any{
		"event_type":      string(evt.Type),
		"tier_from":       string(user.Tier),
		"tier_to":         string(domain.TierFree),
		"customer_id":     sub.CustomerID,
		"subscription_id": sub.SubscriptionID,
	})
}

// applySubscriptionUpdated syncs the stored subscription status and period end from Stripe's
// subscription.updated events (which fire on creation, renewal, past-due transitions, etc.).
//
// A PLAN change on an existing subscription (e.g. a future billing-portal price swap) is deliberately
// NOT mapped to a tier change here: this pass maps upgrades via checkout.session.completed and
// downgrades via customer.subscription.deleted, per the pass-2 scope. subscription.updated only
// keeps the stored status/period end fresh.
func applySubscriptionUpdated(ctx context.Context, users BillingUserStore, events BillingEventStore, evt billing.Event) error {
	sub := evt.Subscription
	if sub == nil {
		return errors.New("billing: subscription.updated without parsed payload")
	}
	if sub.CustomerID == "" {
		return errors.New("billing: subscription.updated without a customer id")
	}

	user, err := users.GetBillingByStripeCustomerID(ctx, sub.CustomerID)
	if err != nil {
		if errors.Is(err, auth.ErrUserNotFound) {
			return recordEvent(ctx, events, evt, "", map[string]any{
				"event_type":      string(evt.Type),
				"customer_id":     sub.CustomerID,
				"subscription_id": sub.SubscriptionID,
				"note":            "customer not found",
			})
		}
		return err
	}

	var periodEnd time.Time
	if sub.CurrentPeriodEnd > 0 {
		periodEnd = time.Unix(sub.CurrentPeriodEnd, 0).UTC()
	}
	if err := users.SetStripeSubscription(ctx, user.UserID, sub.SubscriptionID, sub.Status, periodEnd); err != nil {
		return err
	}

	return recordEvent(ctx, events, evt, user.UserID, map[string]any{
		"event_type":      string(evt.Type),
		"status":          sub.Status,
		"customer_id":     sub.CustomerID,
		"subscription_id": sub.SubscriptionID,
	})
}

// recordInvoiceEvent records invoice.payment_failed / invoice.payment_succeeded. The events are
// deliberately log-only: Stripe's automatic retry schedule is the failed-payment mechanism, and the
// final failure arrives as customer.subscription.deleted, which is where the Free-tier revert
// happens. The recorded row is the durable billing-event trace.
func recordInvoiceEvent(ctx context.Context, users BillingUserStore, events BillingEventStore, evt billing.Event) error {
	inv := evt.Invoice
	userID := ""
	if inv != nil && inv.CustomerID != "" {
		if user, err := users.GetBillingByStripeCustomerID(ctx, inv.CustomerID); err == nil {
			userID = user.UserID
		} else if !errors.Is(err, auth.ErrUserNotFound) {
			return err
		}
		// An unknown customer is still recorded (with empty user_id) — the event itself is the trace.
	}
	return recordEvent(ctx, events, evt, userID, map[string]any{
		"event_type":  string(evt.Type),
		"invoice_id":  inv.InvoiceID,
		"customer_id": inv.CustomerID,
	})
}

// recordEvent writes the success marker for a processed event. Called ONLY after the event's effects
// have been applied; a failure here fails the webhook so Stripe retries.
func recordEvent(ctx context.Context, events BillingEventStore, evt billing.Event, userID string, data map[string]any) error {
	payload, err := json.Marshal(data)
	if err != nil {
		return errors.New("billing: encoding event record")
	}
	if _, err := events.RecordIfNew(ctx, evt.ID, string(evt.Type), userID, payload); err != nil {
		return err
	}
	return nil
}

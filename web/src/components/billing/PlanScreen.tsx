"use client";

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  AlertTriangle,
  CheckCircle2,
  Loader2,
  Lock,
  ShieldCheck,
  XCircle,
} from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button, buttonClassNames } from "@/components/ui/Button";
import { StatusPill, type StatusVariant } from "@/components/ui/StatusPill";
import { SkeletonList } from "@/components/shared/Skeleton";
import { ErrorState } from "@/components/shared/ErrorState";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { SubscriptionTier } from "@/types/schema";
import { PLANS, PlanCard, planActionLabel, type PlanId } from "./PlanCard";

export interface PlanScreenProps {
  /**
   * The user's tier from the session, threaded for parity with the session-convention used across
   * the app. The screen deliberately does NOT render from it: GET /v1/billing/subscription is
   * authoritative, and the session tier can be stale until the Stripe webhook lands after an
   * upgrade. Kept in the type so the server component can pass it uniformly; unused today.
   */
  userTier?: SubscriptionTier;
  /**
   * Threaded from the session for future account-scoped flows (e.g. a Stripe customer portal).
   * Not consumed by this screen today — billing endpoints authenticate via the session cookie.
   */
  userId?: string;
}

/** GET /v1/billing/subscription — the authoritative current-plan snapshot (server/internal/api/billing.go). */
interface GetSubscriptionResponse {
  subscription: {
    plan: string;
    stripe_subscription_exists: boolean;
    stripe_subscription_status?: string;
    stripe_subscription_period_end?: string;
  };
}

/** POST /v1/billing/checkout — a hosted Stripe Checkout session; redirect the browser to `url`. */
interface CheckoutResponse {
  checkout: { session_id: string; url: string };
}

interface SubscriptionState {
  plan: SubscriptionTier;
  stripeSubscriptionExists: boolean;
  stripeSubscriptionStatus: string;
  stripeSubscriptionPeriodEnd: string | null;
}

type LoadState =
  | { status: "loading" }
  | { status: "loaded"; subscription: SubscriptionState }
  | { status: "error"; message: string };

/** The `?upgrade=` outcome Stripe's redirect carries: success or cancelled. */
type UpgradeOutcome = "success" | "cancelled";

const UPGRADE_PARAM = "upgrade";

/** Maps a stored Stripe subscription status to an honest, labelled pill (never color alone). */
function subscriptionStatusPill(status: string): { variant: StatusVariant; label: string } {
  switch (status) {
    case "active":
      return { variant: "success", label: "Active" };
    case "trialing":
      return { variant: "info", label: "Trialing" };
    case "past_due":
      return { variant: "warning", label: "Past due" };
    case "unpaid":
      return { variant: "error", label: "Unpaid" };
    case "incomplete":
    case "incomplete_expired":
      return { variant: "warning", label: "Incomplete" };
    case "canceling":
      return { variant: "warning", label: "Cancelling" };
    case "canceled":
      return { variant: "neutral", label: "Cancelled" };
    default:
      // The server surfaces Stripe's status verbatim (billing.go); an unknown status is shown
      // honestly rather than guessed at.
      return { variant: "neutral", label: status || "Unknown" };
  }
}

/**
 * The account plan & billing screen at `/settings` (billing milestone Pass 3).
 *
 * Reads GET /v1/billing/subscription and renders the current plan plus a Free/Pro/Team comparison
 * (copy from monetization.md's Pricing Tiers). Upgrading/switching POSTs /v1/billing/checkout and
 * redirects the browser to Stripe's hosted Checkout — never an inline Elements form, which this
 * pass deliberately does not build (hosted Checkout needs no publishable key).
 *
 * Stripe's success/cancel redirects land here as `?upgrade=success|cancelled` (the server's
 * success/cancel URL contract in billing.go); the screen shows an honest banner — "your plan is
 * being updated", not an instant assertion — because the webhook tier change can lag the redirect,
 * and then cleans the query param from the URL.
 *
 * The Free plan has no Stripe price (STRIPE_PRICE_FREE is blank): it is selectable only as the
 * current plan. A paid user's path back to Free is cancellation, which the server does NOT expose
 * as an endpoint this pass — the screen says so honestly instead of fabricating one.
 */
export function PlanScreen(props: PlanScreenProps) {
  // The page threads the session tier/userId for parity with the app's session convention, but
  // this screen renders from GET /v1/billing/subscription, which is authoritative — the session
  // tier can be stale until Stripe's webhook lands after an upgrade, so it is deliberately never
  // used as the displayed plan. The props are acknowledged here so their type stays the contract.
  void props.userTier;
  void props.userId;

  const router = useRouter();
  const searchParams = useSearchParams();

  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [outcome, setOutcome] = useState<UpgradeOutcome | null>(null);
  const [checkoutPlan, setCheckoutPlan] = useState<PlanId | null>(null);
  const [checkoutError, setCheckoutError] = useState<string | null>(null);
  const mounted = useRef(true);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const res = await api.get<GetSubscriptionResponse>("/v1/billing/subscription");
      if (!mounted.current) return;
      setState({
        status: "loaded",
        subscription: {
          plan: normalizeSubscriptionPlan(res.subscription.plan),
          stripeSubscriptionExists: res.subscription.stripe_subscription_exists,
          stripeSubscriptionStatus: res.subscription.stripe_subscription_status ?? "",
          stripeSubscriptionPeriodEnd:
            res.subscription.stripe_subscription_period_end ?? null,
        },
      });
    } catch (error) {
      if (!mounted.current) return;
      setState({
        status: "error",
        message:
          error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
    return () => {
      mounted.current = false;
    };
  }, [load]);

  // Read Stripe's ?upgrade= outcome once on mount, show the banner from state, then clean the
  // param so a refresh does not re-show it (same URL discipline as the export dialog).
  useEffect(() => {
    const value = searchParams.get(UPGRADE_PARAM);
    if (value === "success" || value === "cancelled") {
      // Reflecting URL state into component state on mount is a legitimate external-system sync:
      // the banner must survive the router.replace that cleans the param. Same suppression the
      // codebase uses for mount-time state initialization (SharingTab, WorkspaceSettingsScreen).
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setOutcome(value);
      const params = new URLSearchParams(searchParams.toString());
      params.delete(UPGRADE_PARAM);
      const query = params.toString();
      router.replace(
        query ? `${window.location.pathname}?${query}` : window.location.pathname,
        { scroll: false },
      );
    }
    // Intentionally runs once on mount: after the replace clears the param, re-running would do
    // nothing harmful but is unnecessary, and the outcome must not be wiped by a later render.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const startCheckout = useCallback(
    async (plan: PlanId) => {
      if (checkoutPlan) return;
      setCheckoutPlan(plan);
      setCheckoutError(null);
      try {
        const res = await api.post<CheckoutResponse>("/v1/billing/checkout", { plan });
        // Hosted Checkout flow: hand the browser to Stripe. The screen does not render inline —
        // that would require Elements and the publishable key, which this pass deliberately avoids.
        window.location.assign(res.checkout.url);
      } catch (error) {
        setCheckoutPlan(null);
        if (error instanceof ApiError && error.code === "billing_not_configured") {
          // Honest dev-without-Stripe-keys state: the server told us a catalog price is missing.
          setCheckoutError(
            "Billing isn't configured for this plan yet, so checkout can't be started. " +
              "This usually means the Stripe price keys aren't set up in this environment.",
          );
        } else {
          setCheckoutError(
            error instanceof ApiError
              ? error.message
              : "Could not start checkout. Try again.",
          );
        }
      }
    },
    [checkoutPlan],
  );

  if (state.status === "loading") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <SkeletonList count={3} />
      </main>
    );
  }

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <ErrorState description={state.message} onRetry={() => void load()} />
      </main>
    );
  }

  const { subscription } = state;
  const currentPlan = subscription.plan as PlanId;
  const subscribed = subscription.stripeSubscriptionExists;
  const statusPill = subscriptionStatusPill(subscription.stripeSubscriptionStatus);

  return (
    <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
      <h1 className="text-h1 text-text-primary">Plan &amp; Billing</h1>
      <p className="mt-1 text-body-small text-text-secondary">
        Your plan, what it unlocks, and how to change it. The server enforces every tier gate — this
        screen only shows where you stand.
      </p>

      {outcome === "success" && (
        <div
          role="status"
          className="mt-6 flex items-start gap-3 rounded-md border border-success/40 bg-success/10 p-4"
        >
          <CheckCircle2 size={18} className="mt-0.5 shrink-0 text-success" aria-hidden="true" />
          <p className="text-body-small text-text-primary">
            Your plan is being updated. It can take a moment for the change to appear here — if it
            doesn&apos;t show shortly, check back in a minute.
          </p>
        </div>
      )}
      {outcome === "cancelled" && (
        <div
          role="status"
          className="mt-6 flex items-start gap-3 rounded-md border border-border bg-surface-secondary p-4"
        >
          <XCircle size={18} className="mt-0.5 shrink-0 text-text-secondary" aria-hidden="true" />
          <p className="text-body-small text-text-primary">
            Checkout was cancelled — your plan hasn&apos;t changed.
          </p>
        </div>
      )}

      {checkoutError && (
        <div
          role="alert"
          className="mt-6 flex items-start gap-3 rounded-md border border-error/40 bg-error/10 p-4"
        >
          <AlertTriangle size={18} className="mt-0.5 shrink-0 text-error" aria-hidden="true" />
          <p className="text-body-small text-error">{checkoutError}</p>
        </div>
      )}

      <Card className="mt-6">
        <CardHeader
          title="Current plan"
          description="Your plan applies to your whole account. Usage is metered per workspace regardless of plan."
          action={
            <div className="flex items-center gap-2">
              <StatusPill
                variant={planPillVariant(subscription.plan)}
                showDot
              >
                {planLabel(subscription.plan)}
              </StatusPill>
              {subscribed && (
                <StatusPill variant={statusPill.variant}>{statusPill.label}</StatusPill>
              )}
            </div>
          }
        />
        <dl className="grid gap-2 sm:grid-cols-2">
          <div>
            <dt className="text-caption uppercase tracking-wide text-text-secondary">Price</dt>
            <dd className="text-body text-text-primary">{priceFor(subscription.plan)}</dd>
          </div>
          {subscribed && subscription.stripeSubscriptionPeriodEnd && (
            <div>
              <dt className="text-caption uppercase tracking-wide text-text-secondary">
                Current period ends
              </dt>
              <dd className="text-body text-text-primary">
                {formatDate(subscription.stripeSubscriptionPeriodEnd)}
              </dd>
            </div>
          )}
          {subscribed && !subscription.stripeSubscriptionPeriodEnd && (
            <div>
              <dt className="text-caption uppercase tracking-wide text-text-secondary">
                Subscription
              </dt>
              <dd className="text-body text-text-primary">Status: {statusPill.label.toLowerCase()}</dd>
            </div>
          )}
        </dl>

        {subscribed && (
          <div className="mt-4 border-t border-border pt-4">
            <p className="flex items-start gap-2 text-body-small text-text-secondary">
              <Lock size={14} className="mt-0.5 shrink-0 text-text-secondary" aria-hidden="true" />
              <span>
                Cancelling or downgrading is managed through Stripe — there&apos;s no self-serve
                cancel button here yet. Paid access continues until the end of the current billing
                period, then the account reverts to Free tier limits; workspaces beyond the Free
                tier&apos;s single-workspace limit become read-only (never deleted).
              </span>
            </p>
          </div>
        )}
      </Card>

      <h2 className="mt-8 text-h2 text-text-primary">Compare plans</h2>
      <p className="mt-1 text-body-small text-text-secondary">
        Verification, trust, and privacy features are identical on every plan — never paywalled.
        Every plan includes a monthly usage allotment; usage beyond it is billed at pay-as-you-go
        rates rather than blocked.
      </p>

      <div className="mt-6 grid gap-4 md:grid-cols-3">
        {PLANS.map((plan) => {
          const isCurrent = plan.id === currentPlan;
          const checkoutBusy = checkoutPlan === plan.id;
          let action: ReactNode;
          if (isCurrent) {
            action = (
              <Button variant="outline" disabled className="w-full">
                Current plan
              </Button>
            );
          } else if (plan.id === "free") {
            // Free has no Stripe price (STRIPE_PRICE_FREE is blank) — it is never an upgrade
            // target. The path back to Free is cancellation, covered by the note above.
            action = (
              <p className="px-2 py-2 text-center text-caption text-text-secondary">
                Reverts when a paid plan is cancelled
              </p>
            );
          } else {
            action = (
              <Button
                variant={plan.id === "pro" ? "primary" : "secondary"}
                className="w-full"
                loading={checkoutBusy}
                disabled={checkoutBusy}
                onClick={() => void startCheckout(plan.id)}
              >
                {checkoutBusy && <Loader2 size={16} className="animate-spin" aria-hidden="true" />}
                {planActionLabel(plan.id, currentPlan)}
              </Button>
            );
          }
          return (
            <PlanCard
              key={plan.id}
              plan={plan}
              isCurrent={isCurrent}
              highlighted={plan.id === "pro"}
              action={action}
            />
          );
        })}
      </div>

      <div className="mt-8 flex flex-col items-start gap-3 rounded-md border border-border bg-surface p-4 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex items-start gap-3">
          <ShieldCheck size={18} className="mt-0.5 shrink-0 text-success" aria-hidden="true" />
          <p className="text-body-small text-text-secondary">
            Your plan applies account-wide, and every tier gate is enforced server-side. Usage is
            metered per workspace regardless of plan.
          </p>
        </div>
        <Link
          href={routes.workspaces()}
          className={buttonClassNames("outline", "shrink-0")}
        >
          Back to your workspaces
        </Link>
      </div>
    </main>
  );
}

/** The server's plan value is a lower-case tier string; map it to the client union honestly. */
function normalizeSubscriptionPlan(plan: string): SubscriptionTier {
  if (plan === SubscriptionTier.Pro || plan === SubscriptionTier.Team) return plan;
  return SubscriptionTier.Free;
}

/** A current-plan pill: plan name, coloured by whether it is a paid plan in good standing. */
function planPillVariant(plan: SubscriptionTier): StatusVariant {
  return plan === SubscriptionTier.Free ? "neutral" : "info";
}

/** "Free" / "Pro" / "Team" with a plan suffix for the current-plan pill. */
function planLabel(plan: SubscriptionTier): string {
  return plan === SubscriptionTier.Free
    ? "Free plan"
    : plan === SubscriptionTier.Pro
      ? "Pro plan"
      : "Team plan";
}

/** The current plan's headline price line, from the same monetization.md catalog. */
function priceFor(plan: SubscriptionTier): string {
  const info = PLANS.find((p) => p.id === plan);
  return info ? `${info.price} ${info.priceNote}` : "—";
}

/** Formats an RFC3339 period-end timestamp for the current-plan card. */
function formatDate(rfc3339: string): string {
  const date = new Date(rfc3339);
  if (Number.isNaN(date.getTime())) return rfc3339;
  return date.toLocaleDateString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
  });
}

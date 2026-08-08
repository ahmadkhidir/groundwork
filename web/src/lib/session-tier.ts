import { apiUrl } from "@/lib/api-client";
import { SubscriptionTier } from "@/types/schema";

/**
 * Session-tier claim refresh.
 *
 * The Auth.js JWT carries a `tier` claim so tier gates render without an extra round trip. The
 * claim is written at sign-in and would otherwise go stale the moment a Stripe webhook changes the
 * account's plan — the bug where a paid account keeps rendering Free-tier "Upgrade to Pro/Team"
 * prompts until the user signs out and back in. These helpers re-read the authoritative plan from
 * the Go API on a TTL and update the claim in place.
 *
 * Display-only by design: the Go API re-checks entitlement on every gated action, so a stale or
 * failed refresh is a cosmetic staleness, never a security boundary.
 */

/**
 * How long the cached `tier` claim is trusted before the web tier re-reads the authoritative value
 * from the Go API. A one-minute bound keeps the UI converging within a minute of any plan change
 * without hammering the API — at most one cheap, local GET per user per minute.
 */
export const SESSION_TIER_REFRESH_MS = 60_000;

/**
 * Maps the Go API's subscription plan string to the client tier union.
 *
 * Returns null (rather than a guessed tier) for anything unrecognized, so the caller keeps its
 * existing claim instead of overwriting it with a value the server never sent.
 */
export function normalizeSubscriptionTier(
  plan: string | undefined,
): SubscriptionTier | null {
  if (
    plan === SubscriptionTier.Free ||
    plan === SubscriptionTier.Pro ||
    plan === SubscriptionTier.Team
  ) {
    return plan;
  }
  return null;
}

/**
 * Whether the cached tier claim is old enough to warrant a refresh.
 *
 * A token issued before this field existed has `tierCheckedAt` undefined and is treated as stale,
 * so the first session read after a deploy refreshes every pre-existing claim once.
 */
export function isTierStale(tierCheckedAt: number | undefined, now: number): boolean {
  return now - (tierCheckedAt ?? 0) >= SESSION_TIER_REFRESH_MS;
}

/**
 * Re-reads the caller's authoritative plan from the Go API (`GET /v1/billing/subscription`).
 *
 * Server-to-server, authenticated by forwarding the browser's Auth.js session cookie in the Cookie
 * header — the same cookie the browser sends to the Go API directly. `cookieHeader` is
 * `cookies().toString()` from next/headers (the full request Cookie header), so the Go side's
 * `authsession.FromRequest` finds the session it needs.
 *
 * Fail-soft by design: any network, parse, or non-OK failure returns null and the caller keeps the
 * existing display claim. Never throws.
 */
export async function refreshSessionTier(
  cookieHeader: string,
): Promise<SubscriptionTier | null> {
  try {
    const response = await fetch(apiUrl("/v1/billing/subscription"), {
      headers: {
        Accept: "application/json",
        Cookie: cookieHeader,
      },
      // Never let Next's data cache serve a stale subscription read.
      cache: "no-store",
    });
    if (!response.ok) return null;
    const body = (await response.json()) as { subscription?: { plan?: string } };
    return normalizeSubscriptionTier(body.subscription?.plan);
  } catch {
    return null;
  }
}

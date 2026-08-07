import type { Metadata } from "next";
import { PlanScreen } from "@/components/billing/PlanScreen";
import { auth } from "@/auth";

export const metadata: Metadata = { title: "Plan & Billing" };

/**
 * Route: `/settings` — the account-level plan & billing screen (billing milestone Pass 3).
 *
 * Deliberately distinct from `/workspaces/:id/settings` (the per-workspace settings screen). The
 * Go API builds Stripe Checkout's success/cancel redirect URLs against exactly this path
 * (`server/internal/api/billing.go`'s `upgradeSuccessPath` / `upgradeCancelledPath`), so it must
 * never move. The PlanScreen reads the `?upgrade=success|cancelled` query param those redirects
 * carry.
 *
 * Not registered in PUBLIC_ROUTE_PATTERNS: the proxy treats `/settings` as protected, so an
 * unauthenticated visitor is sent through the login gate with a `next` redirect back here — the
 * exact flow a checkout landing demands.
 */
export default async function Page() {
  // The session's tier is a display hint only (web/src/auth.ts); the server's
  // GET /v1/billing/subscription is authoritative for what the screen renders.
  const session = await auth();

  return <PlanScreen userTier={session?.user?.tier} userId={session?.user?.id} />;
}

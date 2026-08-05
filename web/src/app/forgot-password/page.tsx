import type { Metadata } from "next";
import { AuthCard } from "@/components/auth/AuthCard";
import { ForgotPasswordForm } from "@/components/auth/ForgotPasswordForm";

export const metadata: Metadata = { title: "Reset your password" };

/**
 * Route: `/forgot-password` — reached from the login screen's "Forgot password?" link
 * (screens/02-sign-up-login.md), satisfying FEAT-001's "password reset flow is available"
 * acceptance criterion.
 *
 * GAP, reported upstream: not itself documented as a `screens/NN-*.md` file, and NOT YET public in
 * `src/lib/routes.ts`'s PUBLIC_ROUTE_PATTERNS (that file is read-only to this slice) — until that
 * one-line addition lands, `src/proxy.ts` will redirect an unauthenticated visitor here to `/login`
 * before this page ever renders, which defeats its purpose. See the implementor's report for the
 * exact patch.
 */
export default function Page() {
  return (
    <AuthCard
      title="Reset your password"
      toggle={{ href: "/login", prompt: "Remembered it?", label: "Log in" }}
    >
      <ForgotPasswordForm />
    </AuthCard>
  );
}

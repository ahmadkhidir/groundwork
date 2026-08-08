import type { Metadata } from "next";
import { LandingScreen } from "@/components/landing/LandingScreen";
import { auth } from "@/auth";

export const metadata: Metadata = {
  title: "Groundwork — Every number, traced back to the data.",
};

/**
 * Route: `/` — Landing Page, screen 01 (docs/Groundwork/screens/01-landing-page.md).
 *
 * Server-rendered with no client fetch on load (the one SSR/SSG screen per architecture.md). The
 * session is read only for the screens/01 edge case: an already-authenticated visitor still sees
 * the landing page, but the primary CTA becomes "Go to Workspaces" instead of "Get started".
 */
export default async function LandingPage() {
  const session = await auth();
  return <LandingScreen signedIn={Boolean(session)} />;
}

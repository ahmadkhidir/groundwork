import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Sign up" };

/** Route: `/signup` — see docs/Groundwork/screens/02-sign-up-login.md */
export default function Page() {
  return (
    <PlaceholderScreen
      screen="02 — Sign Up"
      spec="screens/02-sign-up-login.md"
      features="FEAT-001"
      phase="Phase 1"
    />
  );
}

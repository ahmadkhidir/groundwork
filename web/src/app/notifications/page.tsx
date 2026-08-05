import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Notifications" };

/** Route: `/notifications` — see docs/Groundwork/screens/16-notifications.md */
export default function Page() {
  return (
    <PlaceholderScreen
      screen="16 — Notifications"
      spec="screens/16-notifications.md"
      features="FEAT-026"
      phase="Phase 5"
    />
  );
}

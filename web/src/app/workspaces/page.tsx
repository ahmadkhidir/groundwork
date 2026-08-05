import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Workspaces" };

/** Route: `/workspaces` — see docs/Groundwork/screens/03-workspace-list.md */
export default function Page() {
  return (
    <PlaceholderScreen
      screen="03 — Workspace List"
      spec="screens/03-workspace-list.md"
      features="FEAT-002"
      phase="Phase 1"
    />
  );
}

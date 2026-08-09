import type { Metadata } from "next";
import { WorkspaceListScreen } from "@/components/workspaces/WorkspaceListScreen";

export const metadata: Metadata = { title: "Workspaces" };

/** Route: `/workspaces` — see docs/Groundwork/screens/03-workspace-list.md */
export default function Page() {
  return <WorkspaceListScreen />;
}

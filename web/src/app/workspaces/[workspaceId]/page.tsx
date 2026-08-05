import type { Metadata } from "next";
import { WorkspaceHomeScreen } from "@/components/workspace/WorkspaceHomeScreen";

export const metadata: Metadata = { title: "Workspace" };

/** Route: `/workspaces/[workspaceId]` — see docs/Groundwork/screens/04-workspace-home.md (FEAT-011) */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <WorkspaceHomeScreen workspaceId={workspaceId} />;
}

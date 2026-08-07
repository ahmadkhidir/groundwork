import type { Metadata } from "next";
import { WorkspaceSettingsScreen } from "@/components/workspace/WorkspaceSettingsScreen";

export const metadata: Metadata = { title: "Settings" };

/** Route: `/workspaces/[workspaceId]/settings` — see docs/Groundwork/screens/13-workspace-settings.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/settings">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <WorkspaceSettingsScreen workspaceId={workspaceId} />;
}

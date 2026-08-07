import type { Metadata } from "next";
import { WorkspaceSettingsScreen } from "@/components/workspace/WorkspaceSettingsScreen";
import { auth } from "@/auth";

export const metadata: Metadata = { title: "Settings" };

/** Route: `/workspaces/[workspaceId]/settings` — see docs/Groundwork/screens/13-workspace-settings.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/settings">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  // The user's tier and id ride on the session as display concerns only (web/src/auth.ts): tier
  // gates which Sharing/Usage surfaces render (screens/13's Upgrade Prompt / totals-only states),
  // and id decides owner vs read-only member treatment. The authoritative checks are always
  // enforced server-side.
  const session = await auth();

  return (
    <WorkspaceSettingsScreen
      workspaceId={workspaceId}
      userTier={session?.user?.tier}
      userId={session?.user?.id}
    />
  );
}

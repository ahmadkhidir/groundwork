import type { Metadata } from "next";
import { WorkspaceHomeScreen } from "@/components/workspace/WorkspaceHomeScreen";
import { auth } from "@/auth";

export const metadata: Metadata = { title: "Workspace" };

/** Route: `/workspaces/[workspaceId]` — see docs/Groundwork/screens/04-workspace-home.md (FEAT-011) */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  // The user id + tier decide owner vs read-only member treatment and the smart-generation
  // affordance (Ask panel + suggestions strip); the server remains authoritative for everything.
  const session = await auth();

  return (
    <WorkspaceHomeScreen
      workspaceId={workspaceId}
      userId={session?.user?.id}
      userTier={session?.user?.tier}
    />
  );
}

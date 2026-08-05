import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Workspace" };

/** Route: `/workspaces/[workspaceId]` — see docs/Groundwork/screens/04-workspace-home.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="04 — Workspace Home"
      spec="screens/04-workspace-home.md"
      features="FEAT-003"
      phase="Phase 1"
      params={params}
    />
  );
}

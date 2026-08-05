import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Settings" };

/** Route: `/workspaces/[workspaceId]/settings` — see docs/Groundwork/screens/13-workspace-settings.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/settings">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="13 — Workspace Settings"
      spec="screens/13-workspace-settings.md"
      features="FEAT-010, FEAT-043, FEAT-047"
      phase="Phase 1"
      params={params}
    />
  );
}

import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Run" };

/** Route: `/workspaces/[workspaceId]/runs/[runId]` — see docs/Groundwork/screens/08-run-viewer.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/runs/[runId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="08 — Run Viewer"
      spec="screens/08-run-viewer.md"
      features="FEAT-013 … FEAT-020"
      phase="Phase 2"
      params={params}
    />
  );
}

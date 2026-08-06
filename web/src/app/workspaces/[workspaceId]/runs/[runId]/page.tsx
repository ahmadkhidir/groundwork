import type { Metadata } from "next";
import { RunViewerScreen } from "@/components/run/RunViewerScreen";

export const metadata: Metadata = { title: "Run" };

/** Route: `/workspaces/[workspaceId]/runs/[runId]` — see docs/Groundwork/screens/08-run-viewer.md */
export default async function Page(
  props: PageProps<"/workspaces/[workspaceId]/runs/[runId]">,
) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId, runId } = await props.params;

  return <RunViewerScreen workspaceId={workspaceId} runId={runId} />;
}
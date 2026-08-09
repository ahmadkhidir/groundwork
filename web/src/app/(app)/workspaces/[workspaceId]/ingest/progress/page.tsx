import type { Metadata } from "next";
import { IngestProgressScreen } from "@/components/ingest/IngestProgressScreen";

export const metadata: Metadata = { title: "Ingesting" };

/**
 * Route: `/workspaces/[workspaceId]/ingest/progress` — see docs/Groundwork/screens/06-ingest-progress.md.
 *
 * FEAT-004 (this wave) drives "Provisioning engine" and "Loading data". "Profiling columns" and
 * "Finding relationships" are structurally present in the step indicator but not yet driven —
 * FEAT-005 and FEAT-008 land in Wave 4. See IngestProgressScreen's doc comment.
 */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/ingest/progress">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <IngestProgressScreen workspaceId={workspaceId} />;
}

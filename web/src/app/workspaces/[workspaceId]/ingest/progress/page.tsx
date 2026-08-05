import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Ingesting" };

/** Route: `/workspaces/[workspaceId]/ingest/progress` — see docs/Groundwork/screens/06-ingest-progress.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/ingest/progress">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="06 — Ingest Progress"
      spec="screens/06-ingest-progress.md"
      features="FEAT-004, FEAT-005"
      phase="Phase 1"
      params={params}
    />
  );
}

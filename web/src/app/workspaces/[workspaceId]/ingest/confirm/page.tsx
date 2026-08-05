import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Confirm ingest" };

/** Route: `/workspaces/[workspaceId]/ingest/confirm` — see docs/Groundwork/screens/07-ingest-confirmation.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/ingest/confirm">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="07 — Ingest Confirmation"
      spec="screens/07-ingest-confirmation.md"
      features="FEAT-006, FEAT-007, FEAT-008, FEAT-009"
      phase="Phase 1"
      params={params}
    />
  );
}

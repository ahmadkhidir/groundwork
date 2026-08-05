import type { Metadata } from "next";
import { IngestConfirmationScreen } from "@/components/confirmation/IngestConfirmationScreen";

export const metadata: Metadata = { title: "Confirm ingest" };

/**
 * Route: `/workspaces/[workspaceId]/ingest/confirm` — see docs/Groundwork/screens/07-ingest-confirmation.md.
 *
 * FEAT-006 (type review), FEAT-007 (PII flag review), and FEAT-009 (candidate join accept/reject).
 */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/ingest/confirm">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <IngestConfirmationScreen workspaceId={workspaceId} />;
}

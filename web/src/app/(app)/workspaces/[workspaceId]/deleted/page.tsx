import type { Metadata } from "next";
import { DeletedProofScreen } from "@/components/workspace/DeletedProofScreen";

export const metadata: Metadata = { title: "Deletion proof" };

/** Route: `/workspaces/[workspaceId]/deleted` — see docs/Groundwork/screens/14-deletion-confirmation-proof.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/deleted">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <DeletedProofScreen workspaceId={workspaceId} />;
}

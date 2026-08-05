import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Deletion proof" };

/** Route: `/workspaces/[workspaceId]/deleted` — see docs/Groundwork/screens/14-deletion-confirmation-proof.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/deleted">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="14 — Deletion Proof"
      spec="screens/14-deletion-confirmation-proof.md"
      features="FEAT-043, FEAT-044"
      phase="Phase 1"
      params={params}
    />
  );
}

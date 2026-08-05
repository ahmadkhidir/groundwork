import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Audit log" };

/** Route: `/workspaces/[workspaceId]/audit-log` — see docs/Groundwork/screens/17-egress-audit-log.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/audit-log">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="17 — Egress / Audit Log"
      spec="screens/17-egress-audit-log.md"
      features="FEAT-042"
      phase="Phase 1"
      params={params}
    />
  );
}

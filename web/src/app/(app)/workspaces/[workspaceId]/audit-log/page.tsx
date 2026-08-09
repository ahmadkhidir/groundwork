import type { Metadata } from "next";
import { AuditLogScreen } from "@/components/audit/AuditLogScreen";

export const metadata: Metadata = { title: "Audit log" };

/** Route: `/workspaces/[workspaceId]/audit-log` — see docs/Groundwork/screens/17-egress-audit-log.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/audit-log">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId } = await props.params;

  return <AuditLogScreen workspaceId={workspaceId} />;
}

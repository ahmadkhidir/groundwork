import type { Metadata } from "next";
import { ReportViewScreen } from "@/components/report/ReportViewScreen";
import { auth } from "@/auth";

export const metadata: Metadata = { title: "Report" };

/** Route: `/workspaces/[workspaceId]/reports/[reportId]` — see docs/Groundwork/screens/09-report-view.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/reports/[reportId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId, reportId } = await props.params;

  // The user's tier rides on the session as a display concern only (web/src/auth.ts). The Export
  // Dialog uses it to render tier-gated format rows (screens/11); the authoritative gate is always
  // enforced server-side on the export endpoint via UserTierLookup.
  const session = await auth();

  return (
    <ReportViewScreen
      workspaceId={workspaceId}
      reportId={reportId}
      userTier={session?.user?.tier}
    />
  );
}
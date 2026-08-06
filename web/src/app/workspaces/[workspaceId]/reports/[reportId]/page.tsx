import type { Metadata } from "next";
import { ReportViewScreen } from "@/components/report/ReportViewScreen";

export const metadata: Metadata = { title: "Report" };

/** Route: `/workspaces/[workspaceId]/reports/[reportId]` — see docs/Groundwork/screens/09-report-view.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/reports/[reportId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { workspaceId, reportId } = await props.params;

  return <ReportViewScreen workspaceId={workspaceId} reportId={reportId} />;
}
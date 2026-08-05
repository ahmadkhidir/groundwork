import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Report" };

/** Route: `/workspaces/[workspaceId]/reports/[reportId]` — see docs/Groundwork/screens/09-report-view.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/reports/[reportId]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="09 — Report View"
      spec="screens/09-report-view.md"
      features="FEAT-034, FEAT-035"
      phase="Phase 4"
      params={params}
    />
  );
}

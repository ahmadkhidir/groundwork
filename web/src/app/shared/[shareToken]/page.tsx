import type { Metadata } from "next";
import { SharedReportScreen } from "@/components/shared/SharedReportScreen";

export const metadata: Metadata = { title: "Shared report" };

/** Route: `/shared/[shareToken]` — screens/09-report-view.md's Read-Only (shared link) state (FEAT-046). */
export default async function Page(props: PageProps<"/shared/[shareToken]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const { shareToken } = await props.params;

  return <SharedReportScreen shareToken={shareToken} />;
}

import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Shared report" };

/** Route: `/shared/[shareToken]` — see docs/Groundwork/screens/09-report-view.md */
export default async function Page(props: PageProps<"/shared/[shareToken]">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="09 — Shared Report (read-only)"
      spec="screens/09-report-view.md"
      features="FEAT-046"
      phase="Phase 5"
      params={params}
    />
  );
}

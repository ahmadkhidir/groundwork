import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Upload data" };

/** Route: `/workspaces/[workspaceId]/upload` — see docs/Groundwork/screens/05-file-upload.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/upload">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="05 — File Upload"
      spec="screens/05-file-upload.md"
      features="FEAT-003, FEAT-004"
      phase="Phase 1"
      params={params}
    />
  );
}

import type { Metadata } from "next";
import { PlaceholderScreen } from "@/components/PlaceholderScreen";

export const metadata: Metadata = { title: "Semantic layer" };

/** Route: `/workspaces/[workspaceId]/semantic-layer` — see docs/Groundwork/screens/15-semantic-layer.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/semantic-layer">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return (
    <PlaceholderScreen
      screen="15 — Semantic Layer"
      spec="screens/15-semantic-layer.md"
      features="FEAT-022, FEAT-023, FEAT-024"
      phase="Phase 3"
      params={params}
    />
  );
}

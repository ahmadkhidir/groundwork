import type { Metadata } from "next";
import { SemanticLayerScreen } from "@/components/semantic/SemanticLayerScreen";

export const metadata: Metadata = { title: "Semantic layer" };

/** Route: `/workspaces/[workspaceId]/semantic-layer` — see docs/Groundwork/screens/15-semantic-layer.md */
export default async function Page(props: PageProps<"/workspaces/[workspaceId]/semantic-layer">) {
  // params is a Promise in Next 16; synchronous access was removed.
  const params = await props.params;

  return <SemanticLayerScreen workspaceId={params.workspaceId} />;
}

import Link from "next/link";
import { Network } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { routes } from "@/lib/routes";

export interface SemanticLayerSummaryPanelProps {
  workspaceId: string;
}

/**
 * Compact semantic layer summary panel — screens/04-workspace-home.md's web-only right column:
 * "12 relationships, 4 metric definitions — view all".
 *
 * screens/04-workspace-home.md documents this panel's data source
 * (`GET /api/workspaces/:id/semantic-layer/summary`) but does not tag it with a feature ID the way
 * the rest of the screen is tagged — reported as a documentation gap. The closest owning feature is
 * FEAT-019 (Semantic Layer Storage, Phase 2), which is what actually populates relationships and
 * metric definitions; that endpoint does not exist yet, so this renders a visibly-unbuilt counts
 * placeholder rather than fabricating numbers, and still links through to the (currently
 * placeholder) Semantic Layer screen.
 */
export function SemanticLayerSummaryPanel({
  workspaceId,
}: SemanticLayerSummaryPanelProps) {
  return (
    <Card>
      <CardHeader
        title="Semantic layer"
        description="Relationships and metric definitions arrive with the Semantic Layer feature (Phase 2)."
      />
      <div className="flex items-center gap-2 text-body-small text-text-secondary">
        <Network size={16} aria-hidden="true" />
        <span>Not built yet</span>
      </div>
      <Link
        href={routes.semanticLayer(workspaceId)}
        className="mt-3 inline-block text-body-small text-primary hover:underline"
      >
        View semantic layer
      </Link>
    </Card>
  );
}

"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Network } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Skeleton } from "@/components/shared/Skeleton";
import { api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { SemanticSummaryResponse } from "@/components/semantic/types";

export interface SemanticLayerSummaryPanelProps {
  workspaceId: string;
}

type PanelState =
  | { status: "loading" }
  | { status: "loaded"; relationshipCount: number; metricDefinitionCount: number }
  | { status: "error" };

/**
 * Compact semantic layer summary panel — screens/04-workspace-home.md's web-only right column:
 * "12 relationships, 4 metric definitions — view all".
 *
 * Now wired to FEAT-019's real GET /v1/workspaces/:id/semantic-layer/summary. Previously rendered a
 * visibly-unbuilt placeholder ("Not built yet") since that endpoint didn't exist; this wave built it.
 */
export function SemanticLayerSummaryPanel({ workspaceId }: SemanticLayerSummaryPanelProps) {
  const [state, setState] = useState<PanelState>({ status: "loading" });

  const load = useCallback(async () => {
    try {
      const res = await api.get<SemanticSummaryResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/summary`);
      setState({
        status: "loaded",
        relationshipCount: res.relationship_count,
        metricDefinitionCount: res.metric_definition_count,
      });
    } catch {
      // A quiet failure here is acceptable: this is a secondary summary panel on a screen whose
      // primary job (engine status, Ask input) does not depend on it, and screens/04 gives it no
      // dedicated Error state of its own — showing a dash rather than the full shared Error State
      // avoids a second, redundant error block on an otherwise-working Workspace Home.
      setState({ status: "error" });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  return (
    <Card>
      <CardHeader title="Semantic layer" description="Relationships and metric definitions grounding your questions." />
      <div className="flex items-center gap-2 text-body-small text-text-secondary">
        <Network size={16} aria-hidden="true" />
        {state.status === "loading" && <Skeleton className="h-4 w-40" />}
        {state.status === "error" && <span>Counts unavailable right now.</span>}
        {state.status === "loaded" && (
          <span>
            {state.relationshipCount} relationship{state.relationshipCount === 1 ? "" : "s"},{" "}
            {state.metricDefinitionCount} metric definition{state.metricDefinitionCount === 1 ? "" : "s"}
          </span>
        )}
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

"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Card } from "@/components/ui/Card";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import type { ConfirmationColumn, ListColumnsResponse } from "@/components/confirmation/types";
import type { Cardinality } from "@/types/schema";
import { RelationshipsTab } from "./RelationshipsTab";
import { MetricDefinitionsTab } from "./MetricDefinitionsTab";
import { CorrectionsTab } from "./CorrectionsTab";
import {
  SEMANTIC_TABS,
  SEMANTIC_TAB_LABELS,
  type SemanticTab,
  type SemanticRelationship,
  type SemanticMetricDefinition,
  type SemanticCorrection,
  type ListRelationshipsResponse,
  type ListMetricDefinitionsResponse,
  type ListCorrectionsResponse,
  type UpdateRelationshipResponse,
  type MetricDefinitionResponse,
} from "./types";

export interface SemanticLayerScreenProps {
  workspaceId: string;
}

/** Per-tab fetch state. Each tab loads independently, on first activation (screens/15's Data Flow). */
type TabState<T> =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "loaded"; items: T[] }
  | { status: "error"; message: string };

function itemsOf<T>(state: TabState<T>): T[] {
  return state.status === "loaded" ? state.items : [];
}

/**
 * Screen 15: Semantic Layer / Corrections (screens/15-semantic-layer.md) — FEAT-019 (this wave) and
 * FEAT-020 (a later wave; the Corrections tab here is legitimately read-only and often empty until
 * then — see CorrectionsTab.tsx's doc comment).
 *
 * Each of the three tabs (Relationships, Metric Definitions, Corrections) fetches its own data lazily
 * on first activation, per screens/15's documented Data Flow, rather than all three loading up front.
 */
export function SemanticLayerScreen({ workspaceId }: SemanticLayerScreenProps) {
  const [activeTab, setActiveTab] = useState<SemanticTab>("relationships");

  const [relationshipsState, setRelationshipsState] = useState<TabState<SemanticRelationship>>({ status: "idle" });
  const [metricsState, setMetricsState] = useState<TabState<SemanticMetricDefinition>>({ status: "idle" });
  const [correctionsState, setCorrectionsState] = useState<TabState<SemanticCorrection>>({ status: "idle" });

  // Column id -> ConfirmationColumn, so the Relationships tab can label columns as
  // "filename: column_name" instead of raw ids — the same lookup ingest confirmation's
  // JoinReviewStep already uses for the equivalent CandidateJoin display.
  const [columnsById, setColumnsById] = useState<Map<string, ConfirmationColumn>>(new Map());

  const [savingIds, setSavingIds] = useState<Set<string>>(new Set());
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});

  function markSaving(id: string, saving: boolean) {
    setSavingIds((prev) => {
      const next = new Set(prev);
      if (saving) next.add(id);
      else next.delete(id);
      return next;
    });
  }

  function setRowError(id: string, message: string) {
    setRowErrors((prev) => ({ ...prev, [id]: message }));
  }

  function clearRowError(id: string) {
    setRowErrors((prev) => {
      if (!(id in prev)) return prev;
      const next = { ...prev };
      delete next[id];
      return next;
    });
  }

  const loadRelationships = useCallback(async () => {
    setRelationshipsState({ status: "loading" });
    try {
      const [relRes, colRes] = await Promise.all([
        api.get<ListRelationshipsResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/relationships`),
        api.get<ListColumnsResponse>(`/v1/workspaces/${workspaceId}/columns`),
      ]);
      setColumnsById(new Map((colRes.columns ?? []).map((c) => [c.id, c])));
      setRelationshipsState({ status: "loaded", items: relRes.relationships ?? [] });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setRelationshipsState({ status: "error", message });
    }
  }, [workspaceId]);

  const loadMetrics = useCallback(async () => {
    setMetricsState({ status: "loading" });
    try {
      const res = await api.get<ListMetricDefinitionsResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/metrics`);
      setMetricsState({ status: "loaded", items: res.metric_definitions ?? [] });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setMetricsState({ status: "error", message });
    }
  }, [workspaceId]);

  const loadCorrections = useCallback(async () => {
    setCorrectionsState({ status: "loading" });
    try {
      const res = await api.get<ListCorrectionsResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/corrections`);
      setCorrectionsState({ status: "loaded", items: res.corrections ?? [] });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setCorrectionsState({ status: "error", message });
    }
  }, [workspaceId]);

  // Lazy-load: fetch a tab's data the first time it becomes active, not before. Each load*
  // function's own setState is a mount-triggered async fetch, the same pattern
  // WorkspaceHomeScreen.tsx and IngestConfirmationScreen.tsx already use — not a synchronous
  // in-effect setState, so the lint rule below is suppressed for the same reason it is there.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    if (activeTab === "relationships" && relationshipsState.status === "idle") void loadRelationships();
    if (activeTab === "metrics" && metricsState.status === "idle") void loadMetrics();
    if (activeTab === "corrections" && correctionsState.status === "idle") void loadCorrections();
  }, [
    activeTab,
    relationshipsState.status,
    metricsState.status,
    correctionsState.status,
    loadRelationships,
    loadMetrics,
    loadCorrections,
  ]);

  async function handleUpdateRelationship(id: string, cardinality: Cardinality, grainNotes: string) {
    markSaving(id, true);
    clearRowError(id);
    try {
      const res = await api.patch<UpdateRelationshipResponse>(
        `/v1/workspaces/${workspaceId}/semantic-layer/relationships/${id}`,
        { cardinality, grain_notes: grainNotes },
      );
      setRelationshipsState((current) =>
        current.status === "loaded"
          ? { ...current, items: current.items.map((r) => (r.id === id ? res.relationship : r)) }
          : current,
      );
    } catch (error) {
      // screens/15's Error state: "failed edit reverts to prior value with an inline error" — local
      // state is never mutated on failure, so the row already shows its last-known-good value.
      const message = error instanceof ApiError ? error.message : "Could not save this change. Try again.";
      setRowError(id, message);
    } finally {
      markSaving(id, false);
    }
  }

  async function handleCreateMetric(fields: { name: string; sqlExpression: string; description: string }) {
    const res = await api.post<MetricDefinitionResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/metrics`, {
      name: fields.name,
      sql_expression: fields.sqlExpression,
      description: fields.description,
    });
    setMetricsState((current) =>
      current.status === "loaded" ? { ...current, items: [...current.items, res.metric_definition] } : current,
    );
  }

  async function handleUpdateMetric(
    id: string,
    fields: { name: string; sqlExpression: string; description: string },
  ) {
    const res = await api.patch<MetricDefinitionResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/metrics/${id}`, {
      name: fields.name,
      sql_expression: fields.sqlExpression,
      description: fields.description,
    });
    setMetricsState((current) =>
      current.status === "loaded"
        ? { ...current, items: current.items.map((m) => (m.id === id ? res.metric_definition : m)) }
        : current,
    );
  }

  async function handleDeleteMetric(id: string) {
    markSaving(id, true);
    clearRowError(id);
    try {
      await api.delete(`/v1/workspaces/${workspaceId}/semantic-layer/metrics/${id}`);
      setMetricsState((current) =>
        current.status === "loaded" ? { ...current, items: current.items.filter((m) => m.id !== id) } : current,
      );
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not delete this metric. Try again.";
      setRowError(id, message);
    } finally {
      markSaving(id, false);
    }
  }

  // FEAT-020 AC3: removing a stored correction. Screens/15's Error state — "failed edit reverts to
  // prior value with an inline error" — falls out naturally: state is only mutated on success, so a
  // failed delete leaves the row in place and surfaces the message inline.
  async function handleDeleteCorrection(id: string) {
    markSaving(id, true);
    clearRowError(id);
    try {
      await api.delete(`/v1/workspaces/${workspaceId}/semantic-layer/corrections/${id}`);
      setCorrectionsState((current) =>
        current.status === "loaded" ? { ...current, items: current.items.filter((c) => c.id !== id) } : current,
      );
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not delete this correction. Try again.";
      setRowError(id, message);
    } finally {
      markSaving(id, false);
    }
  }

  const activeState: TabState<unknown> = useMemo(() => {
    switch (activeTab) {
      case "relationships":
        return relationshipsState;
      case "metrics":
        return metricsState;
      case "corrections":
        return correctionsState;
    }
  }, [activeTab, relationshipsState, metricsState, correctionsState]);

  function retryActiveTab() {
    if (activeTab === "relationships") void loadRelationships();
    if (activeTab === "metrics") void loadMetrics();
    if (activeTab === "corrections") void loadCorrections();
  }

  return (
    <main id="main-content" className="mx-auto flex w-full max-w-5xl flex-col gap-6 p-8">
      <div>
        <h1 className="text-h1 text-text-primary">Semantic layer</h1>
        <p className="mt-1 text-body-small text-text-secondary">
          Relationships, metric definitions, and accumulated corrections that ground every question
          in this workspace.
        </p>
      </div>

      <div role="tablist" aria-label="Semantic layer tabs" className="flex gap-1 border-b border-border">
        {SEMANTIC_TABS.map((tab) => (
          <button
            key={tab}
            type="button"
            role="tab"
            aria-selected={tab === activeTab}
            onClick={() => setActiveTab(tab)}
            className={
              "touch-target px-4 py-2 text-body font-medium " +
              (tab === activeTab
                ? "border-b-2 border-primary text-primary"
                : "text-text-secondary hover:text-text-primary")
            }
          >
            {SEMANTIC_TAB_LABELS[tab]}
          </button>
        ))}
      </div>

      <Card>
        {(activeState.status === "loading" || activeState.status === "idle") && <SkeletonList count={3} />}

        {activeState.status === "error" && <ErrorState onRetry={retryActiveTab} />}

        {activeState.status === "loaded" && (
          <>
            {activeTab === "relationships" && (
              <RelationshipsTab
                relationships={itemsOf(relationshipsState)}
                columnsById={columnsById}
                savingIds={savingIds}
                errors={rowErrors}
                onUpdate={handleUpdateRelationship}
              />
            )}
            {activeTab === "metrics" && (
              <MetricDefinitionsTab
                metricDefinitions={itemsOf(metricsState)}
                savingIds={savingIds}
                onCreate={handleCreateMetric}
                onUpdate={handleUpdateMetric}
                onDelete={handleDeleteMetric}
              />
            )}
            {activeTab === "corrections" && (
              <CorrectionsTab
                workspaceId={workspaceId}
                corrections={itemsOf(correctionsState)}
                savingIds={savingIds}
                errors={rowErrors}
                onDelete={handleDeleteCorrection}
              />
            )}
          </>
        )}
      </Card>
    </main>
  );
}

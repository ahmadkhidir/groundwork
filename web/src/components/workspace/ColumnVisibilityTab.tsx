"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { EyeOff, Lock, TableProperties } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { StatusPill } from "@/components/ui/StatusPill";
import { EmptyState } from "@/components/shared/EmptyState";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { upgradeLabelFor } from "@/lib/tier";
import { SubscriptionTier, type MetricDefinition } from "@/types/schema";
import {
  datasetGroupLabel,
  groupColumnsByDataset,
  type ConfirmationColumn,
  type ListColumnsResponse,
} from "@/components/confirmation/types";

export interface ColumnVisibilityTabProps {
  workspaceId: string;
  /**
   * The user's tier for DISPLAY — column exclusion (FEAT-010) is Pro/Team per monetization.md.
   * On Free, the exclusion toggle renders the shared Upgrade Prompt (screens.md) rather than a
   * control the server would reject. Display-only: the authoritative gate is server-side via
   * UserTierLookup. Defaults to Free (the most restrictive display), matching ExportDialog's
   * convention — the caller threads the real tier.
   */
  tier?: SubscriptionTier;
}

interface ListMetricDefinitionsResponse {
  metric_definitions: MetricDefinition[];
}

interface UpdateColumnResponse {
  column: ConfirmationColumn;
}

type LoadState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string };

/** A column whose exclusion toggle is waiting on the metric-reference confirm (screens/13 edge case). */
interface PendingExclusion {
  column: ConfirmationColumn;
  next: boolean;
}

/**
 * A per-column exclusion toggle row's local error — separate from the shared ErrorState so one
 * failed PATCH does not take down the whole tab (screens/13: "for a failed toggle update
 * specifically, an inline error with the toggle reverted to its prior state").
 */
type RowErrors = Record<string, string>;

/** True when a metric definition's SQL expression references the column by name. */
function metricReferencesColumn(metric: MetricDefinition, columnName: string): boolean {
  if (!columnName) return false;
  // A word-boundary match so a column named "id" does not light up inside "paid_amount". Escaped so
  // a column name with regex characters cannot throw.
  const escaped = columnName.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`\\b${escaped}\\b`, "i").test(metric.sql_expression);
}

/** How many metric definitions reference the column — the count screens/13's warning names. */
function metricReferenceCount(metrics: MetricDefinition[], columnName: string): number {
  return metrics.filter((metric) => metricReferencesColumn(metric, columnName)).length;
}

/**
 * FEAT-010's Column Visibility tab (screens/13-workspace-settings.md): every ingested column
 * workspace-wide, with a per-column "exclude from model" toggle and the automatic PII flag shown for
 * context. The toggle PATCHes the column's `excluded_from_model` — the column stays queryable by the
 * engine; exclusion governs egress to the model.
 *
 * Data flow per screens/13: optimistic UI update, PATCH request, rollback with an inline error on
 * failure. Edge case: excluding a column referenced by an existing metric definition shows a warning
 * before confirming ("This column is used in 2 metric definitions — excluding it may affect future
 * answers"), never a silent block.
 */
export function ColumnVisibilityTab({ workspaceId, tier = SubscriptionTier.Free }: ColumnVisibilityTabProps) {
  const [state, setState] = useState<LoadState>({ status: "loading" });
  const [columns, setColumns] = useState<ConfirmationColumn[]>([]);
  const [metrics, setMetrics] = useState<MetricDefinition[]>([]);
  const [savingIds, setSavingIds] = useState<Set<string>>(new Set());
  const [pendingExclusion, setPendingExclusion] = useState<PendingExclusion | null>(null);
  const [rowErrors, setRowErrors] = useState<RowErrors>({});
  const mounted = useRef(true);
  // FEAT-010 is Pro/Team (monetization.md): on Free the exclusion toggle is a locked Upgrade
  // Prompt. The PII flag and column list stay visible — PII flagging is on every tier.
  const canExclude = tier !== SubscriptionTier.Free;

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const [cols, metricRes] = await Promise.all([
        api.get<ListColumnsResponse>(`/v1/workspaces/${workspaceId}/columns`),
        // Metrics back the screens/13 exclusion warning ("used in N metric definitions"). A failure
        // here degrades to "no warning" — never a fabricated count — so it is fetched together with
        // the columns but tolerated on error.
        api
          .get<ListMetricDefinitionsResponse>(`/v1/workspaces/${workspaceId}/semantic-layer/metrics`)
          .catch(() => ({ metric_definitions: [] as MetricDefinition[] })),
      ]);
      if (!mounted.current) return;
      setColumns(cols.columns);
      setMetrics(metricRes.metric_definitions);
      setRowErrors({});
      setState({ status: "loaded" });
    } catch (error) {
      if (!mounted.current) return;
      const message =
        error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId]);

  useEffect(() => {
    void load();
  }, [load]);

  const applyExclusion = useCallback(
    async (column: ConfirmationColumn, next: boolean) => {
      const previous = columns.map((c) => (c.id === column.id ? { ...c, excluded_from_model: next } : c));
      setColumns(previous);
      setSavingIds((ids) => new Set(ids).add(column.id));
      setRowErrors((errors) => {
        const copy = { ...errors };
        delete copy[column.id];
        return copy;
      });
      try {
        const res = await api.patch<UpdateColumnResponse>(
          `/v1/workspaces/${workspaceId}/columns/${column.id}/exclusion`,
          { excluded_from_model: next },
        );
        setColumns((current) =>
          current.map((c) => (c.id === column.id ? { ...c, excluded_from_model: res.column.excluded_from_model } : c)),
        );
      } catch (error) {
        // Roll back to the prior state and show the inline error, per screens/13.
        setColumns((current) =>
          current.map((c) => (c.id === column.id ? { ...c, excluded_from_model: column.excluded_from_model } : c)),
        );
        setRowErrors((errors) => ({
          ...errors,
          [column.id]: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
        }));
      } finally {
        setSavingIds((ids) => {
          const copy = new Set(ids);
          copy.delete(column.id);
          return copy;
        });
      }
    },
    [columns, workspaceId],
  );

  const handleToggle = useCallback(
    (column: ConfirmationColumn, next: boolean) => {
      // screens/13 edge case: excluding a column referenced by metric definitions warns first.
      if (next && metricReferenceCount(metrics, column.name) > 0) {
        setPendingExclusion({ column, next });
        return;
      }
      void applyExclusion(column, next);
    },
    [applyExclusion, metrics],
  );

  if (state.status === "error") {
    return <ErrorState description={state.message} onRetry={() => void load()} />;
  }

  if (state.status === "loading") {
    return (
      <Card>
        <CardHeader
          title="Column visibility"
          description="Loading columns across every dataset in this workspace…"
        />
        <SkeletonList count={4} />
      </Card>
    );
  }

  if (columns.length === 0) {
    return (
      <EmptyState
        icon={TableProperties}
        title="No columns yet"
        subtitle="Upload data to see every ingested column here, with per-column control over what the model can see."
      />
    );
  }

  const groups = groupColumnsByDataset(columns);
  const pending = pendingExclusion;

  return (
    <Card>
      <CardHeader
        title="Column visibility"
        description="Every ingested column workspace-wide. Excluding a column keeps it queryable by the engine but never sends its name or values to the model."
      />

      {pending && (
        <div
          role="alert"
          className="mb-4 rounded-md border border-warning bg-warning/12 p-3"
        >
          <p className="text-body-small text-text-primary">
            This column is used in {metricReferenceCount(metrics, pending.column.name)}{" "}
            metric definition{pending.next && metricReferenceCount(metrics, pending.column.name) === 1 ? "" : "s"} —
            excluding it may affect future answers.
          </p>
          <div className="mt-2 flex gap-2">
            <Button
              variant="primary"
              onClick={() => {
                setPendingExclusion(null);
                void applyExclusion(pending.column, pending.next);
              }}
            >
              Exclude anyway
            </Button>
            <Button variant="outline" onClick={() => setPendingExclusion(null)}>
              Keep visible
            </Button>
          </div>
        </div>
      )}

      <div className="hidden md:block">
        <table className="w-full border-collapse">
          <thead>
            <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
              <th className="px-2 py-2 font-medium">Column</th>
              <th className="px-2 py-2 font-medium">Type</th>
              <th className="px-2 py-2 font-medium">PII flag</th>
              <th className="px-2 py-2 font-medium">Excluded from model</th>
            </tr>
          </thead>
          {groups.map((group) => (
            <tbody key={group.datasetId}>
              <tr className="bg-surface-secondary">
                <th
                  colSpan={4}
                  scope="colgroup"
                  className="px-2 py-2 text-left text-body-small font-medium text-text-primary"
                >
                  {datasetGroupLabel(group)}
                </th>
              </tr>
              {group.columns.map((col) => (
                <tr key={col.id} className="border-b border-border last:border-b-0 align-top">
                  <td className="px-2 py-2 font-medium text-text-primary">{col.name}</td>
                  <td className="px-2 py-2 text-text-secondary">{col.inferred_type}</td>
                  <td className="px-2 py-2">
                    {col.pii_flag ? (
                      <StatusPill variant="warning">{col.pii_pattern || "flagged"}</StatusPill>
                    ) : (
                      <StatusPill variant="neutral">not flagged</StatusPill>
                    )}
                  </td>
                  <td className="px-2 py-2">
                    <ExclusionControl
                      column={col}
                      loading={savingIds.has(col.id)}
                      error={rowErrors[col.id]}
                      locked={!canExclude}
                      onToggle={handleToggle}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          ))}
        </table>
      </div>

      <div className="flex flex-col gap-4 md:hidden">
        {groups.map((group) => (
          <div key={group.datasetId}>
            <h4 className="mb-2 text-caption font-medium uppercase tracking-wide text-text-secondary">
              {datasetGroupLabel(group)}
            </h4>
            <ul className="flex flex-col gap-3">
              {group.columns.map((col) => (
                <li key={col.id} className="rounded-md border border-border p-3">
                  <div className="mb-2 flex items-start justify-between gap-2">
                    <span className="font-medium text-text-primary">{col.name}</span>
                    {col.pii_flag ? (
                      <StatusPill variant="warning">{col.pii_pattern || "flagged"}</StatusPill>
                    ) : (
                      <StatusPill variant="neutral">not flagged</StatusPill>
                    )}
                  </div>
                  <p className="mb-2 text-body-small text-text-secondary">{col.inferred_type}</p>
                  <ExclusionControl
                    column={col}
                    loading={savingIds.has(col.id)}
                    error={rowErrors[col.id]}
                    locked={!canExclude}
                    onToggle={handleToggle}
                  />
                </li>
              ))}
            </ul>
          </div>
        ))}
      </div>
    </Card>
  );
}

function ExclusionToggle({
  column,
  loading,
  error,
  onToggle,
}: {
  column: ConfirmationColumn;
  loading: boolean;
  error?: string;
  onToggle: (column: ConfirmationColumn, next: boolean) => void;
}) {
  return (
    <div className="flex flex-col items-start gap-1">
      <button
        type="button"
        role="switch"
        aria-checked={column.excluded_from_model}
        aria-label={`Exclude ${column.name} from model visibility`}
        disabled={loading}
        onClick={() => onToggle(column, !column.excluded_from_model)}
        className={
          column.excluded_from_model
            ? "inline-flex items-center gap-1 rounded-sm bg-error/12 px-2 py-1 text-body-small font-medium text-error hover:bg-error/20 disabled:opacity-50"
            : "inline-flex items-center gap-1 rounded-sm border border-border px-2 py-1 text-body-small text-text-secondary hover:bg-surface-secondary disabled:opacity-50"
        }
      >
        <EyeOff size={14} aria-hidden="true" />
        {column.excluded_from_model ? "Excluded" : "Visible"}
      </button>
      {error && <span className="text-body-small text-error">{error}</span>}
    </div>
  );
}

/**
 * The exclusion cell for one column: the real toggle on Pro/Team, or — on Free, where FEAT-010 is
 * locked — the shared Upgrade Prompt (screens.md): the option stays visible but reduced-emphasis
 * with a lock icon and an "Upgrade to Pro/Team" link to the account plan screen. Never a control
 * the server would reject with a 403.
 */
function ExclusionControl({
  column,
  loading,
  error,
  locked,
  onToggle,
}: {
  column: ConfirmationColumn;
  loading: boolean;
  error?: string;
  locked: boolean;
  onToggle: (column: ConfirmationColumn, next: boolean) => void;
}) {
  if (locked) {
    return (
      <Link
        href={routes.accountSettings()}
        aria-label={`${upgradeLabelFor(SubscriptionTier.Pro)} to exclude ${column.name} from model visibility`}
        className="inline-flex items-center gap-1 rounded-sm border border-border px-2 py-1 text-body-small text-text-secondary hover:bg-surface-secondary"
      >
        <Lock size={12} aria-hidden="true" />
        {upgradeLabelFor(SubscriptionTier.Pro)}
      </Link>
    );
  }
  return (
    <ExclusionToggle column={column} loading={loading} error={error} onToggle={onToggle} />
  );
}

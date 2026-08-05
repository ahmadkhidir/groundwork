"use client";

import { useCallback, useEffect, useState } from "react";
import { Activity } from "lucide-react";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/shared/EmptyState";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { AuditFilterChips } from "./AuditFilterChips";
import { AuditEntryCard, AuditEntryTableRow } from "./AuditEntryRow";
import { api } from "@/lib/api-client";
import type { AuditActionTypeFilter, AuditLogEntryWithScope, AuditLogListResponse } from "./types";

export interface AuditLogScreenProps {
  workspaceId: string;
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; entries: AuditLogEntryWithScope[]; nextCursor: string; loadingMore: boolean }
  | { status: "error" };

/** Screen 17: Egress / Audit Log (screens/17-egress-audit-log.md) — FEAT-042. */
export function AuditLogScreen({ workspaceId }: AuditLogScreenProps) {
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const [actionType, setActionType] = useState<AuditActionTypeFilter | "">("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [expandedId, setExpandedId] = useState<string | null>(null);

  const buildQuery = useCallback(
    (cursor?: string) => {
      const params = new URLSearchParams();
      if (actionType) params.set("action_type", actionType);
      if (dateFrom) params.set("date_from", new Date(dateFrom).toISOString());
      if (dateTo) params.set("date_to", new Date(dateTo).toISOString());
      if (cursor) params.set("cursor", cursor);
      const query = params.toString();
      return `/v1/workspaces/${workspaceId}/audit-log${query ? `?${query}` : ""}`;
    },
    [workspaceId, actionType, dateFrom, dateTo],
  );

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const response = await api.get<AuditLogListResponse>(buildQuery());
      setState({
        status: "loaded",
        entries: response.entries ?? [],
        nextCursor: response.next_cursor ?? "",
        loadingMore: false,
      });
    } catch {
      setState({ status: "error" });
    }
  }, [buildQuery]);

  // Changing a filter re-fetches from the first page — never filters an already-loaded, unbounded
  // local set — per screens/17's Data Flow. See WorkspaceListScreen.tsx for why the disable below
  // is a deliberate false-positive suppression, not a rule violation: react-hooks/set-state-in-effect
  // cannot tell an async fetch-then-setState apart from a synchronous in-effect setState.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  async function loadMore() {
    if (state.status !== "loaded" || !state.nextCursor) return;
    setState({ ...state, loadingMore: true });
    try {
      const response = await api.get<AuditLogListResponse>(buildQuery(state.nextCursor));
      setState((current) =>
        current.status === "loaded"
          ? {
              status: "loaded",
              entries: [...current.entries, ...(response.entries ?? [])],
              nextCursor: response.next_cursor ?? "",
              loadingMore: false,
            }
          : current,
      );
    } catch (error) {
      // "Load more" failing leaves the existing list intact rather than replacing it with an error
      // state — only the initial fetch does that.
      setState((current) =>
        current.status === "loaded" ? { ...current, loadingMore: false } : current,
      );
      void error;
    }
  }

  const hasActiveFilters = actionType !== "" || dateFrom !== "" || dateTo !== "";

  return (
    <main id="main-content" className="mx-auto w-full max-w-4xl p-8">
      <h1 className="text-h1 text-text-primary">Egress &amp; audit log</h1>
      <p className="mt-1 text-body-small text-text-secondary">
        Every payload sent to the model, every query, export, deletion, and login — the single
        visible record of what left this workspace.
      </p>

      <div className="mt-6 flex flex-col gap-4">
        <AuditFilterChips value={actionType} onChange={setActionType} />

        <div className="hidden items-end gap-2 sm:flex">
          <label className="flex flex-col gap-1 text-body-small text-text-secondary">
            From
            <input
              type="date"
              value={dateFrom}
              onChange={(e) => setDateFrom(e.target.value)}
              className="h-(--size-input-height) rounded-md border border-border bg-surface px-2 text-body text-text-primary"
            />
          </label>
          <label className="flex flex-col gap-1 text-body-small text-text-secondary">
            To
            <input
              type="date"
              value={dateTo}
              onChange={(e) => setDateTo(e.target.value)}
              className="h-(--size-input-height) rounded-md border border-border bg-surface px-2 text-body text-text-primary"
            />
          </label>
        </div>
      </div>

      <div className="mt-4">
        {state.status === "loading" && <SkeletonList count={4} />}

        {state.status === "error" && <ErrorState onRetry={() => void load()} />}

        {state.status === "loaded" && state.entries.length === 0 && !hasActiveFilters && (
          <EmptyState
            icon={Activity}
            title="No activity yet"
            subtitle="Actions will appear here as you use this workspace."
          />
        )}

        {state.status === "loaded" && state.entries.length === 0 && hasActiveFilters && (
          <div className="flex flex-col items-center gap-2 py-12 text-center">
            <p className="text-h3 text-text-primary">No matching activity</p>
            <button
              type="button"
              onClick={() => {
                setActionType("");
                setDateFrom("");
                setDateTo("");
              }}
              className="text-body-small text-provenance underline decoration-dotted underline-offset-2 hover:decoration-solid"
            >
              Clear filters
            </button>
          </div>
        )}

        {state.status === "loaded" && state.entries.length > 0 && (
          <>
            <div className="sm:hidden">
              <ul className="flex flex-col gap-2">
                {state.entries.map((entry) => (
                  <AuditEntryCard
                    key={entry.id}
                    entry={entry}
                    expanded={expandedId === entry.id}
                    onToggle={() => setExpandedId((id) => (id === entry.id ? null : entry.id))}
                  />
                ))}
              </ul>
            </div>

            <div className="hidden sm:block">
              <table className="w-full border-collapse">
                <thead>
                  <tr className="border-b border-border text-left text-caption uppercase tracking-wide text-text-secondary">
                    <th className="px-2 py-2 font-medium">Timestamp</th>
                    <th className="px-2 py-2 font-medium">User</th>
                    <th className="px-2 py-2 font-medium">Action</th>
                    <th className="px-2 py-2 font-medium">Scope</th>
                  </tr>
                </thead>
                <tbody>
                  {state.entries.map((entry) => (
                    <AuditEntryTableRow
                      key={entry.id}
                      entry={entry}
                      expanded={expandedId === entry.id}
                      onToggle={() => setExpandedId((id) => (id === entry.id ? null : entry.id))}
                    />
                  ))}
                </tbody>
              </table>
            </div>

            {state.nextCursor && (
              <div className="mt-4 flex justify-center">
                <Button variant="ghost" onClick={loadMore} loading={state.loadingMore}>
                  Load more
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </main>
  );
}

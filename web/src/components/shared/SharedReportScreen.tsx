"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { Info } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { buttonClassNames } from "@/components/ui/Button";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { ChartRenderer } from "@/components/report/ChartRenderer";
import { AccessibleDataTable } from "@/components/report/AccessibleDataTable";
import { ReconciliationNotice } from "@/components/report/ReconciliationNotice";
import { ProvenanceLinkedClaim } from "@/components/report/ProvenanceLinkedClaim";
import type { ExecuteStepOutput } from "@/components/run/step-payloads";
import { api, ApiError } from "@/lib/api-client";
import { loginWithRedirect } from "@/lib/routes";
import type { ChartSpec, ProvenanceLink, ReconciliationSummary } from "@/types/schema";

export interface SharedReportScreenProps {
  shareToken: string;
}

/**
 * The PUBLIC shape served by GET /v1/shared/reports/:shareToken — the shareable report payload.
 * Deliberately the same rendering surface as screens/09's report, minus workspace-internal ids the
 * server already strips (workspace_id, run_id, parent_report_id).
 */
interface SharedReport {
  id: string;
  title: string;
  narrative: string;
  chart_spec: ChartSpec;
  provenance_links: ProvenanceLink[];
  reconciliation_summary: ReconciliationSummary;
  created_at: string;
}

interface GetSharedReportResponse {
  report: SharedReport;
  /** The execute facts the chart draws from; null when the run's execute output is gone. */
  execute_result: ExecuteStepOutput | null;
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; report: SharedReport; executeResult: ExecuteStepOutput | null }
  | { status: "error"; message: string };

/**
 * Screen 9's Read-Only (shared link) state (screens/09-report-view.md, FEAT-046): the same report
 * content — narrative, chart, provenance-linked numbers, reconciliation notice — reached through an
 * unauthenticated `/shared/[shareToken]` link. Access is by token possession, not account identity.
 *
 * Per screens/09, "Ask a follow-up" and "Export" are hidden and replaced with a "Sign in to
 * interact" prompt. Provenance-linked figures are rendered but not clickable into the authenticated
 * provenance panel; clicking one shows a sign-in hint instead. A revoked or unknown link renders the
 * dedicated not-found state ("This shared report link is no longer available or has been revoked").
 */
export function SharedReportScreen({ shareToken }: SharedReportScreenProps) {
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const [claimHint, setClaimHint] = useState<ProvenanceLink | null>(null);
  const loaded = useRef(false);

  const load = useCallback(async () => {
    try {
      const res = await api.get<GetSharedReportResponse>(
        `/v1/shared/reports/${encodeURIComponent(shareToken)}`,
      );
      loaded.current = true;
      setState({ status: "loaded", report: res.report, executeResult: res.execute_result });
    } catch (error) {
      if (loaded.current) return;
      const message =
        error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [shareToken]);

  useEffect(() => {
    loaded.current = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <ErrorState
          title="Shared report unavailable"
          description={state.message}
          onRetry={() => void load()}
        />
      </main>
    );
  }

  if (state.status === "loading") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="mt-6 h-24 w-full" />
        <Skeleton className="mt-6 h-64 w-full" />
      </main>
    );
  }

  const { report, executeResult } = state;

  return (
    <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
      {/* screens/09 Read-Only state: the interact actions are replaced by a sign-in prompt. */}
      <div
        role="note"
        className="mb-6 flex items-start justify-between gap-3 rounded-md border border-border bg-surface-secondary p-3"
      >
        <p className="flex items-center gap-2 text-body-small text-text-secondary">
          <Info size={16} className="shrink-0 text-info" aria-hidden="true" />
          This is a read-only shared report.
        </p>
        <Link
          href={loginWithRedirect(typeof window !== "undefined" ? window.location.pathname : "/shared")}
          className={buttonClassNames("outline", "shrink-0")}
        >
          Sign in to interact
        </Link>
      </div>

      <header className="mb-6">
        <h1 className="text-h1 text-text-primary">{report.title}</h1>
        <p className="mt-1 text-caption uppercase tracking-wide text-text-secondary">
          {new Date(report.created_at).toLocaleDateString()}
        </p>
      </header>

      <ReconciliationNotice summary={report.reconciliation_summary} />

      <Card className="mt-6">
        <ProvenanceLinkedClaim
          narrative={report.narrative}
          links={report.provenance_links}
          onClaimClick={(link) => setClaimHint(link)}
        />
      </Card>

      {claimHint && (
        <p className="mt-2 rounded-md border border-border bg-surface-secondary p-3 text-body-small text-text-secondary">
          Sign in to view the source data behind “{claimHint.claim_text}”.
        </p>
      )}

      <Card className="mt-6">
        <CardHeader title={report.chart_spec.title || "Result"} />
        <ChartRenderer spec={report.chart_spec} result={executeResult} />
        {executeResult && (
          <details className="mt-4">
            <summary className="cursor-pointer text-body-small text-text-secondary hover:text-text-primary">
              View as table
            </summary>
            <div className="mt-3">
              <AccessibleDataTable
                result={executeResult}
                caption={`Data for ${report.chart_spec.title || "this chart"}`}
              />
            </div>
          </details>
        )}
      </Card>
    </main>
  );
}

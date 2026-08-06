"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { ChartRenderer } from "./ChartRenderer";
import { AccessibleDataTable } from "./AccessibleDataTable";
import { ReconciliationNotice } from "./ReconciliationNotice";
import {
  parseExecuteStep,
  type ExecuteStepOutput,
} from "@/components/run/step-payloads";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { isForkedReport, type Report, type Run, type Step } from "@/types/schema";

export interface ReportViewScreenProps {
  workspaceId: string;
  reportId: string;
}

interface GetReportResponse {
  report: Report;
}

interface GetRunResponse {
  run: Run;
  steps: Step[];
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; report: Report; executeResult: ExecuteStepOutput | null }
  | { status: "error"; message: string };

/**
 * Screen 9: Report View (screens/09-report-view.md) — FEAT-017 (first-party chart rendering).
 *
 * Renders the narrated, charted answer to a question with reconciliation transparency. The report
 * object is fully self-contained (architecture.md's Report Store), but rendering its chart needs the
 * run's result data — the chart is a spec over the execute step's facts — so this screen fetches the
 * report AND its run's steps, then draws the chart first-party with ChartRenderer.
 *
 * Phase-3+ survivors are out of scope this wave: clickable provenance links, the export dialog, and
 * the follow-up (fork) affordance. The report renders read-only with a "View source run" link.
 */
export function ReportViewScreen({ workspaceId, reportId }: ReportViewScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const loaded = useRef(false);

  const load = useCallback(async () => {
    try {
      // The report is the self-contained primary fetch. The run is a secondary fetch used ONLY to
      // recover the execute facts the chart draws from; it may be missing or not-yet-materialized
      // without breaking the report (whose narrative is self-contained).
      const { report } = await api.get<GetReportResponse>(
        `/v1/workspaces/${workspaceId}/reports/${reportId}`,
      );
      let executeResult: ExecuteStepOutput | null = null;
      if (report.run_id) {
        try {
          const { steps } = await api.get<GetRunResponse>(
            `/v1/workspaces/${workspaceId}/runs/${report.run_id}`,
          );
          executeResult = parseExecuteStep(
            steps.find((step) => step.step_name === "execute")?.output,
          );
        } catch {
          // A run fetch failure must not take down the report view.
        }
      }
      loaded.current = true;
      setState({ status: "loaded", report, executeResult });
    } catch (error) {
      if (loaded.current) return;
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId, reportId]);

  useEffect(() => {
    loaded.current = false;
    // Mount-triggered async fetch; see WorkspaceHomeScreen.tsx for why this eslint rule is
    // suppressed here.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <ErrorState
          description={state.message}
          onRetry={() => {
            void load();
          }}
          onGoBack={() => router.push(routes.workspace(workspaceId))}
        />
      </main>
    );
  }

  if (state.status === "loading") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <nav aria-label="Report navigation" className="mb-4">
          <div className="inline-flex items-center gap-1 text-body-small text-text-secondary">
            <ArrowLeft size={14} aria-hidden="true" />
            Back to workspace
          </div>
        </nav>
        <Skeleton className="h-8 w-72" />
        <Skeleton className="mt-6 h-24 w-full" />
        <Skeleton className="mt-6 h-64 w-full" />
      </main>
    );
  }

  const { report, executeResult } = state;

  return (
    <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
      <nav aria-label="Report navigation" className="mb-4">
        <Link
          href={routes.workspace(workspaceId)}
          className="inline-flex items-center gap-1 text-body-small text-text-secondary hover:text-text-primary"
        >
          <ArrowLeft size={14} aria-hidden="true" />
          Back to workspace
        </Link>
      </nav>

      <header className="mb-6">
        <h1 className="text-h1 text-text-primary">{report.title}</h1>
        <p className="mt-1 text-caption uppercase tracking-wide text-text-secondary">
          {new Date(report.created_at).toLocaleDateString()}
        </p>
        {isForkedReport(report) && report.parent_report_id && (
          <Link
            href={routes.report(workspaceId, report.parent_report_id)}
            className="mt-1 inline-block text-body-small text-provenance hover:text-provenance"
          >
            Forked from an earlier report →
          </Link>
        )}
      </header>

      <ReconciliationNotice summary={report.reconciliation_summary} />

      <Card className="mt-6">
        <p className="text-body-large whitespace-pre-line text-text-primary">
          {report.narrative}
        </p>
      </Card>

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

      <div className="mt-4">
        <Link
          href={routes.run(workspaceId, report.run_id)}
          className="inline-flex items-center gap-1 text-body-small text-provenance hover:text-provenance"
        >
          View source run →
        </Link>
      </div>
    </main>
  );
}
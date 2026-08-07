"use client";

import { Suspense, useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { Button, buttonClassNames } from "@/components/ui/Button";
import { Card, CardHeader } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { ChartRenderer } from "./ChartRenderer";
import { AccessibleDataTable } from "./AccessibleDataTable";
import { ReconciliationNotice } from "./ReconciliationNotice";
import { ProvenanceLinkedClaim } from "./ProvenanceLinkedClaim";
import { ProvenancePanel } from "./ProvenancePanel";
import { ExportDialog } from "./ExportDialog";
import {
  parseExecuteStep,
  type ExecuteStepOutput,
} from "@/components/run/step-payloads";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import {
  isForkedReport,
  SubscriptionTier,
  type ProvenanceLink,
  type Report,
  type Run,
  type Step,
} from "@/types/schema";

export interface ReportViewScreenProps {
  workspaceId: string;
  reportId: string;
  /**
   * The user's tier for DISPLAY (which Export Dialog format rows are locked). Display-only — the
   * authoritative tier gate is server-side via UserTierLookup, never a client-claimed tier. When the
   * page does not pass one, the Export Dialog defaults to the most restrictive (Free) display.
   */
  userTier?: SubscriptionTier;
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
 * Screen 9: Report View (screens/09-report-view.md) — FEAT-017 (first-party chart rendering),
 * FEAT-035/036 entry point (Export → the Export Dialog via `?export=` URL state, mirroring the
 * `?claim=` provenance panel), and FEAT-041 (mobile-responsive report viewing).
 *
 * Renders the narrated, charted answer to a question with reconciliation transparency. The report
 * object is fully self-contained (architecture.md's Report Store), but rendering its chart needs the
 * run's result data — the chart is a spec over the execute step's facts — so this screen fetches the
 * report AND its run's steps, then draws the chart first-party with ChartRenderer.
 *
 * Layout per screens/09: mobile gets a bottom action bar ("Export" + "Ask a follow-up") with 44px
 * touch targets and a full-width chart; web gets the reading column beside a persistent side action
 * panel (Export, Ask a follow-up, View source run).
 *
 * Every traceable figure in the narrative is a Provenance-Linked Number (FEAT-030): clicking one
 * opens the provenance panel (screens/10) via `?claim=:claimId` — the panel state is linkable and
 * shareable, exactly as screens/10 requires.
 */
export function ReportViewScreen({ workspaceId, reportId, userTier }: ReportViewScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const loaded = useRef(false);
  // FEAT-021's report-scoped follow-up: "Ask a follow-up" opens a question input on this report;
  // submitting creates a run linked to this report and routes to its Run Viewer.
  const [followUpOpen, setFollowUpOpen] = useState(false);
  const [followUpDraft, setFollowUpDraft] = useState("");
  const [submittingFollowUp, setSubmittingFollowUp] = useState(false);

  const openClaim = useCallback(
    (link: ProvenanceLink) => {
      router.push(`${routes.report(workspaceId, reportId)}?claim=${encodeURIComponent(link.id)}`);
    },
    [router, workspaceId, reportId],
  );

  const openExport = useCallback(() => {
    // URL state, exactly like the provenance panel: the Export Dialog is linkable and survives
    // reload (routes.reportExport builds ?export=1; QueryParam.Export owns the parameter name).
    router.push(routes.reportExport(workspaceId, reportId));
  }, [router, workspaceId, reportId]);

  // FEAT-021: a follow-up question is report-scoped. The run records this report as its parent
  // (parent_report_id), so the answer materializes as a NEW linked report — the original report and
  // its run stay fully intact, and report history reads as a tree.
  const handleFollowUpSubmit = useCallback(async () => {
    const question = followUpDraft.trim();
    if (!question) return;
    setSubmittingFollowUp(true);
    try {
      const { run: nextRun } = await api.post<{ run: Run }>(
        `/v1/workspaces/${workspaceId}/runs`,
        {
          question_text: question,
          parent_report_id: reportId,
        },
      );
      router.push(routes.run(workspaceId, nextRun.id));
    } catch {
      // A failed submission leaves the input open with the draft intact.
    } finally {
      setSubmittingFollowUp(false);
    }
  }, [router, workspaceId, reportId, followUpDraft]);

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
  // The report "has a chart" for the Export Dialog's purposes when the execute facts exist — the
  // same condition ChartRenderer uses to draw anything at all. A narrative-only report is the
  // screens/11 edge case that omits the (Phase 5) Image row; this phase it only varies the PDF row
  // description.
  const hasChart = executeResult !== null;
  // FEAT-021 is Pro/Team (monetization.md). Display-only lock for the Free tier: the server is
  // authoritative and returns 403 upgrade_required regardless; this is the honest UI treatment.
  const followUpLocked = userTier === SubscriptionTier.Free;

  return (
    <main
      id="main-content"
      className="mx-auto w-full max-w-6xl px-4 py-6 pb-24 lg:px-8 lg:py-8 lg:pb-8"
    >
      <div className="lg:flex lg:items-start lg:gap-8">
        {/* Reading column: the narrative is the primary reading surface, so its column stays at a
            legible measure even though the outer container widened to host the side panel. */}
        <div className="min-w-0 flex-1 lg:max-w-3xl">
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
            <ProvenanceLinkedClaim
              narrative={report.narrative}
              links={report.provenance_links}
              onClaimClick={openClaim}
            />
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

          {/* "View source run" for mobile/tablet (the web side panel carries it on lg+). */}
          <div className="mt-4 lg:hidden">
            <Link
              href={routes.run(workspaceId, report.run_id)}
              className="inline-flex items-center gap-1 text-body-small text-provenance hover:text-provenance"
            >
              View source run →
            </Link>
          </div>
        </div>

        {/* Persistent side action panel (web): screens/09's web layout. Sticky so the actions stay
            reachable while the report scrolls. */}
        <aside
          data-testid="report-action-panel"
          aria-label="Report actions"
          className="hidden w-64 shrink-0 lg:block"
        >
          <div className="sticky top-6 space-y-2">
            <Button
              variant="primary"
              className="w-full justify-start"
              onClick={openExport}
            >
              Export
            </Button>
            <Button
              variant="outline"
              className="w-full justify-start"
              onClick={() => setFollowUpOpen((open) => !open)}
              disabled={followUpLocked}
              aria-expanded={followUpOpen}
            >
              {/* screens/09's "Ask a follow-up" affordance. Report-scoped (FEAT-021): the input
                  below posts a run with this report as its parent, and the resulting report links
                  back here. Pro/Team only (monetization.md); the server enforces it. */}
              {followUpLocked ? "Ask a follow-up (Pro)" : "Ask a follow-up"}
            </Button>
            {followUpOpen && (
              <div className="flex flex-col gap-2 rounded-md border border-border bg-surface-secondary p-3">
                <Input
                  label="Follow-up question"
                  value={followUpDraft}
                  onChange={(event) => setFollowUpDraft(event.target.value)}
                  disabled={submittingFollowUp}
                  placeholder="e.g. break this down by month"
                  data-testid="followup-input-web"
                />
                <Button
                  variant="primary"
                  loading={submittingFollowUp}
                  disabled={followUpDraft.trim().length === 0}
                  onClick={handleFollowUpSubmit}
                >
                  Ask
                </Button>
              </div>
            )}
            <Link
              href={routes.run(workspaceId, report.run_id)}
              className={buttonClassNames("ghost", "w-full justify-start")}
            >
              View source run
            </Link>
          </div>
        </aside>
      </div>

      {/* Mobile bottom action bar (screens/09): fixed, with 44px touch targets via .touch-target. */}
      <div
        data-testid="report-mobile-actions"
        className="fixed inset-x-0 bottom-0 z-40 border-t border-border bg-surface p-3 md:hidden"
      >
        {followUpOpen && (
          <div className="mx-auto mb-3 flex w-full max-w-3xl gap-2">
            <Input
              label="Follow-up question"
              value={followUpDraft}
              onChange={(event) => setFollowUpDraft(event.target.value)}
              disabled={submittingFollowUp}
              placeholder="e.g. break this down by month"
              data-testid="followup-input-mobile"
            />
            <Button
              variant="primary"
              loading={submittingFollowUp}
              disabled={followUpDraft.trim().length === 0}
              onClick={handleFollowUpSubmit}
              className="shrink-0"
            >
              Ask
            </Button>
          </div>
        )}
        <div className="mx-auto flex w-full max-w-3xl gap-3">
          <Button variant="primary" touchTarget className="flex-1" onClick={openExport}>
            Export
          </Button>
          <Button
            variant="outline"
            touchTarget
            className="flex-1"
            onClick={() => setFollowUpOpen((open) => !open)}
            disabled={followUpLocked}
            aria-expanded={followUpOpen}
          >
            {/* screens/09's mobile "Ask a follow-up". Report-scoped (FEAT-021): the input above
                posts a run with this report as its parent. Pro/Team only (monetization.md). */}
            {followUpLocked ? "Ask a follow-up (Pro)" : "Ask a follow-up"}
          </Button>
        </div>
      </div>

      {/* useSearchParams must sit inside a Suspense boundary during prerender (Next 16). The panels
          render nothing when their URL params are absent, so the fallbacks are harmlessly empty. */}
      <Suspense fallback={null}>
        <ProvenancePanel workspaceId={workspaceId} reportId={reportId} />
      </Suspense>
      <Suspense fallback={null}>
        <ExportDialog
          workspaceId={workspaceId}
          reportId={reportId}
          tier={userTier}
          hasChart={hasChart}
        />
      </Suspense>
    </main>
  );
}
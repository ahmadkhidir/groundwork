"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft, Sparkles } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { StatusPill, runStatusPill } from "@/components/ui/StatusPill";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { EditableCodeBlock } from "./EditableCodeBlock";
import { StepFeed } from "./StepFeed";
import { ResultTable } from "./ResultTable";
import {
  parseExecuteStep,
  parseGenerateSQLStep,
  parsePlanStep,
} from "./step-payloads";
import { api, ApiError, subscribeToStream } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import {
  RunStatus,
  type Run,
  type Step,
  type StepName,
  type StepStatus,
} from "@/types/schema";

export interface RunViewerScreenProps {
  workspaceId: string;
  runId: string;
}

interface GetRunResponse {
  run: Run;
  steps: Step[];
  /**
   * Id of a report this run materialized, attached on completion (FEAT-018). Null while running or
   * when no report exists; the viewer uses it to auto-navigate to the report and to offer a
   * "View report" link.
   */
  report_id?: string | null;
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; run: Run; steps: Step[]; reportId: string | null }
  | { status: "error"; message: string };

/**
 * FEAT-023's live run-event stream types (server/internal/events). Events carry sparse fields and are
 * written JSON-only (no SSE `event:` channel), so the browser's generic `onmessage` handler receives
 * each as JSON text.
 */
type StreamEventType =
  | "run.status"
  | "step.started"
  | "step.completed"
  | "step.failed"
  | "step.halted"
  | "cache.hit"
  | "stream.end";

interface StreamEvent {
  type: StreamEventType;
  run_id?: string;
  status?: RunStatus;
  step_name?: StepName;
  step_id?: string;
  step_status?: StepStatus;
  output?: string;
  cached_run_id?: string;
}

/**
 * Screen 8: Run Viewer (screens/08-run-viewer.md) — FEAT-013's viewer, live-stream form (FEAT-023).
 *
 * Renders a run's state from the persisted run + steps, opening an SSE stream
 * (GET /v1/workspaces/:id/runs/:runId/stream) so each DAG transition pushes in as it happens instead
 * of being polled. The stream is best-effort — the durable source of truth is the persisted rows the
 * initial (and any reconnect-time) GET reads — so a dropped or timed-out connection (the server's 60s
 * request ceiling, an accepted Pass-1 limitation) is an EventSource auto-reconnect, never a lost
 * update.
 *
 * Phase 2 boundary: halt, amend, and "Resume from here" (FEAT-022/024) and the Clarifying Question
 * surface (FEAT-033) are later. The viewer is watch-only for the current run with FEAT-018's one
 * editing affordance: the generated SQL is editable and "Run edited query" starts a NEW run seeded
 * with that SQL. On completion the viewer auto-navigates to the run's report when one exists. A run
 * answered from determinism cache (FEAT-027) is surfaced with a "cached from run" affordance so a
 * replayed answer is never mistaken for freshly computed.
 */
export function RunViewerScreen({ workspaceId, runId }: RunViewerScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  // Whether at least one read has landed a loaded state, so a transient fetch failure keeps the
  // last-known state on screen instead of flashing the error state.
  const loaded = useRef(false);
  // Guards the completion auto-navigate so it fires once per mount.
  const navigatedToReport = useRef(false);
  // The prior run a determinism-cache hit was replayed from (the SSE cache.hit's cached_run_id), for
  // the "answered from cache" banner. Null on a fresh execution.
  const [cachedFrom, setCachedFrom] = useState<string | null>(null);
  // Busy state while "Run edited query" is submitting the new run.
  const [submittingEdit, setSubmittingEdit] = useState(false);

  const load = useCallback(async () => {
    try {
      const response = await api.get<GetRunResponse>(
        `/v1/workspaces/${workspaceId}/runs/${runId}`,
      );
      const reportId = response.report_id ?? null;
      setState({
        status: "loaded",
        run: response.run,
        steps: response.steps,
        reportId,
      });
      loaded.current = true;
      // FEAT-018 auto-navigate: a run that completed materialized a report, and the viewer follows
      // the user there.
      if (
        response.run.status === RunStatus.Complete &&
        reportId &&
        !navigatedToReport.current
      ) {
        navigatedToReport.current = true;
        router.push(routes.report(workspaceId, reportId));
      }
    } catch (error) {
      if (loaded.current) return;
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [router, workspaceId, runId]);

  // FEAT-018: "Run edited query" starts a NEW run seeded with the edited SQL and hands the user to
  // its viewer. The current run is never mutated.
  const handleRunEdited = useCallback(
    async (sql: string) => {
      const trimmed = sql.trim();
      if (!trimmed) return;
      setSubmittingEdit(true);
      try {
        const { run: nextRun } = await api.post<{ run: Run }>(
          `/v1/workspaces/${workspaceId}/runs`,
          {
            question_text: state.status === "loaded" ? state.run.question_text : "",
            sql: trimmed,
          },
        );
        router.push(routes.run(workspaceId, nextRun.id));
      } catch {
        // A failed submission leaves the user on the viewer with the edit intact; the server's 4xx
        // body would otherwise be surfaced by a dedicated error surface in a later wave.
      } finally {
        setSubmittingEdit(false);
      }
    },
    [router, workspaceId, state],
  );

  // Apply one live event to the loaded state. Runs resolve to per-step upserts by step name, so an
  // out-of-order or duplicated event never double-counts a transition.
  const applyStreamEvent = useCallback(
    (ev: StreamEvent) => {
      switch (ev.type) {
        case "run.status": {
          const status = ev.status;
          if (!status) return;
          setState((prev) =>
            prev.status === "loaded" && prev.run.status !== status
              ? { ...prev, run: { ...prev.run, status } }
              : prev,
          );
          // On completion, refresh once to pick up the materialized report_id and auto-navigate.
          if (status === RunStatus.Complete) void load();
          break;
        }
        case "cache.hit":
          if (ev.cached_run_id) setCachedFrom(ev.cached_run_id);
          break;
        case "step.started":
        case "step.completed":
        case "step.failed":
        case "step.halted": {
          if (!ev.step_name || !ev.step_status) return;
          const name = ev.step_name;
          setState((prev) => {
            if (prev.status !== "loaded") return prev;
            const existing = prev.steps.find((s) => s.step_name === name);
            if (existing) {
              return {
                ...prev,
                steps: prev.steps.map((s) =>
                  s.step_name === name
                    ? { ...s, status: ev.step_status!, output: ev.output ?? s.output }
                    : s,
                ),
              };
            }
            return {
              ...prev,
              steps: [
                ...prev.steps,
                {
                  id: ev.step_id || `${prev.run.id}:${name}`,
                  run_id: prev.run.id,
                  step_name: name,
                  status: ev.step_status!,
                  output: ev.output || undefined,
                  idempotency_key: "",
                } as Step,
              ],
            };
          });
          break;
        }
        default:
          break;
      }
    },
    [load],
  );

  useEffect(() => {
    loaded.current = false;
    navigatedToReport.current = false;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setCachedFrom(null);
    // Mount-triggered async fetch; see WorkspaceHomeScreen.tsx for why this eslint rule is
    // suppressed here.
    void load();

    // Live stream (FEAT-023). EventSource auto-reconnects on error; the server sends stream.end
    // after the run reaches a terminal state, and each run.status event also lets us stop watching.
    const unsubscribe = subscribeToStream(
      `/v1/workspaces/${workspaceId}/runs/${runId}/stream`,
      {
        onMessage: (data) => {
          try {
            applyStreamEvent(JSON.parse(data) as StreamEvent);
          } catch {
            // Ignore a malformed message; the stream re-runs on the next reconnect.
          }
        },
        onError: () => {
          // EventSource reconnects automatically; the durable GET remains the source of truth.
        },
      },
    );
    return () => unsubscribe();
  }, [workspaceId, runId, load, applyStreamEvent]);

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <ErrorState
          description={state.message}
          onRetry={() => {
            loaded.current = false;
            setCachedFrom(null);
            void load();
          }}
          onGoBack={() => router.push(routes.workspace(workspaceId))}
        />
      </main>
    );
  }

  const run = state.status === "loaded" ? state.run : null;
  const steps = state.status === "loaded" ? state.steps : [];
  const reportId = state.status === "loaded" ? state.reportId : null;

  // Parse the three Phase 2 step payloads at the boundary (schema.ts's convention on Step).
  const planStep = steps.find((step) => step.step_name === "plan");
  const plan = planStep ? parsePlanStep(planStep.output) : null;
  const generateSQLStep = steps.find((step) => step.step_name === "generate_sql");
  const generateSQL = generateSQLStep ? parseGenerateSQLStep(generateSQLStep.output) : null;
  const executeStep = steps.find((step) => step.step_name === "execute");
  const executeResult = executeStep ? parseExecuteStep(executeStep.output) : null;

  const runPill = run ? runStatusPill(run.status) : null;
  const question = run?.question_text ?? "";

  // Phase 2 form of the Clarification Needed state: the plan step completed saying the question is
  // not answerable, so the run failed — surface the reason as a banner.
  const notAnswerable =
    run?.status === RunStatus.Failed &&
    plan !== null &&
    plan.answerable === false;
  const clarificationReason =
    plan?.reason?.trim() || "This question couldn't be answered from the available data.";

  return (
    <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
      <nav aria-label="Run navigation" className="mb-4">
        <Link
          href={routes.workspace(workspaceId)}
          className="inline-flex items-center gap-1 text-body-small text-text-secondary hover:text-text-primary"
        >
          <ArrowLeft size={14} aria-hidden="true" />
          Back to workspace
        </Link>
      </nav>

      <div className="mb-6 flex flex-wrap items-center justify-between gap-4">
        <div className="min-w-0">
          <p className="text-caption uppercase tracking-wide text-text-secondary">Run</p>
          {run ? (
            <h1 className="mt-1 text-h1 text-text-primary">{question}</h1>
          ) : (
            <Skeleton className="mt-2 h-8 w-72" />
          )}
        </div>
        {runPill ? (
          <StatusPill variant={runPill.variant} showDot>
            {runPill.label}
          </StatusPill>
        ) : (
          <StatusPill variant="neutral">Loading…</StatusPill>
        )}
      </div>

      {run?.status === RunStatus.Waking && (
        <p className="mb-4 text-body text-text-secondary">
          Waking engine… your question is queued and will run as soon as the engine is ready.
        </p>
      )}

      {cachedFrom && (
        <div
          role="status"
          className="mb-4 flex items-center gap-2 rounded-md border border-provenance bg-provenance/12 p-3 text-body-small text-text-primary"
        >
          <Sparkles size={16} className="shrink-0 text-provenance" aria-hidden="true" />
          <p>
            Answered instantly from a prior identical run
            {cachedFrom ? <span className="text-provenance"> (cached from run {cachedFrom.slice(0, 8)}…)</span> : null}.
            This is a replayed answer, not freshly computed.
          </p>
        </div>
      )}

      {reportId && run?.status === RunStatus.Complete && (
        <Link
          href={routes.report(workspaceId, reportId)}
          className="mb-4 inline-block text-body-small text-provenance hover:text-provenance"
        >
          View report →
        </Link>
      )}

      {notAnswerable && (
        <div
          role="alert"
          className="mb-4 rounded-md border border-warning bg-warning/12 p-3 text-body-small text-text-primary"
        >
          <p className="font-medium">This question needs clarification</p>
          <p className="mt-1 text-text-secondary">{clarificationReason}</p>
        </div>
      )}

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[1fr_1.5fr]">
        <Card>
          <CardHeader
            title="Steps"
            description="Plan → generate SQL → execute. Verification steps (static check, reconcile, shadow check) arrive in later phases."
          />
          {run ? <StepFeed steps={steps} /> : <Skeleton className="h-32" />}
        </Card>

        <div className="flex flex-col gap-6">
          <Card>
            <EditableCodeBlock
              label="Generated SQL"
              code={generateSQL?.sql ?? ""}
              editable={Boolean(generateSQL?.sql)}
              hint="Editing runs exactly this SQL in a new run."
              onRunEdited={handleRunEdited}
              submitting={submittingEdit}
            />
          </Card>

          {/* Screen 15's entry point: "Run Viewer — link after a correction is saved during a run." A
              seeded run produced by "Run edited query" has no plan step (FEAT-018) and generated SQL —
              that is exactly a run whose edited query was captured as a durable correction (FEAT-020). */}
          {run && !planStep && generateSQL && (
            <Link
              href={routes.semanticLayer(workspaceId)}
              className="text-body-small text-primary hover:underline"
            >
              Correction saved — view it in the semantic layer →
            </Link>
          )}

          {run && executeResult && (
            <Card>
              <CardHeader
                title="Result"
                description={`${executeResult.row_count} row${executeResult.row_count === 1 ? "" : "s"}`}
              />
              <ResultTable result={executeResult} />
            </Card>
          )}
        </div>
      </div>
    </main>
  );
}
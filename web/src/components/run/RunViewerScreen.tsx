"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft, GitBranch, Sparkles } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { StatusPill, runStatusPill } from "@/components/ui/StatusPill";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { EditableCodeBlock } from "./EditableCodeBlock";
import { StepFeed } from "./StepFeed";
import { ResultTable } from "./ResultTable";
import { ClarifyingQuestionCard } from "./ClarifyingQuestionCard";
import {
  parseExecuteStep,
  parseGenerateSQLStep,
  parsePlanStep,
} from "./step-payloads";
import { api, ApiError, subscribeToStream } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import {
  RunStatus,
  isForkedRun,
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
  /**
   * Runs forked from this one (FEAT-025's branch/tree presentation). Empty for a run with no
   * branches; the parent link a forked run carries is the other half of the tree.
   */
  forked_runs?: Run[];
}

type ScreenState =
  | { status: "loading" }
  | {
      status: "loaded";
      run: Run;
      steps: Step[];
      reportId: string | null;
      forkedRuns: Run[];
    }
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
  // Only a run that COMPLETES WHILE THIS VIEWER IS WATCHING (the assemble-transition arriving over
  // the live stream) auto-navigates to its report. A run already complete on mount was navigated to
  // deliberately (e.g. the report page's "View source run"), so re-firing the redirect there would
  // trap the user in a report↔run loop — the exact defect this flag prevents.
  const completionObserved = useRef(false);
  // The stream endpoint replays the run's CURRENT persisted status as a run.status snapshot, sent as
  // the first message of each SSE connection (live transitions follow after). That snapshot is the
  // state the viewer JOINED — never a transition to arm on — so once per connection the first
  // run.status message is skipped as the snapshot. Reset in onopen so an auto-reconnect after
  // stream.end starts its own connection fresh.
  const runStatusSnapshotSeen = useRef(false);
  // The prior run a determinism-cache hit was replayed from (the SSE cache.hit's cached_run_id), for
  // the "answered from cache" banner. Null on a fresh execution.
  const [cachedFrom, setCachedFrom] = useState<string | null>(null);
  // Busy state while "Run edited query" is submitting the new run.
  const [submittingEdit, setSubmittingEdit] = useState(false);
  // Busy state while a clarifying answer (FEAT-033) is being submitted, and the resolved answer
  // once submitted (screen 12's Resolved state collapses the card to a summary).
  const [submittingClarify, setSubmittingClarify] = useState(false);
  const [clarifiedAnswer, setClarifiedAnswer] = useState<string | null>(null);

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
        forkedRuns: response.forked_runs ?? [],
      });
      loaded.current = true;
      // FEAT-018 auto-navigate: a run that completed while the viewer was watching materialized a
      // report, and the viewer follows the user there. Gate on completionObserved so an
      // already-complete run visited deliberately (report page's "View source run") stays put.
      if (
        completionObserved.current &&
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

  // FEAT-018/FEAT-025: "Run edited query" starts a NEW run seeded with the edited SQL and hands the
  // user to its viewer. The current run is never mutated. The new run is a FORK of the viewed run
  // (parent_run_id), so amendment history reads as a branch/tree — the original stays fully intact.
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
            parent_run_id: runId,
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
    [router, workspaceId, runId, state],
  );

  // FEAT-033: the user answered the clarifying question. POST the answer; the server persists it
  // (so the same ambiguity is not re-asked in this context) and resumes the run from generate_sql.
  // On acceptance the card collapses to the Resolved summary and the live stream reconnects to
  // watch the resumed pipeline.
  const handleClarifySubmit = useCallback(
    async (answer: string) => {
      setSubmittingClarify(true);
      try {
        await api.post(`/v1/workspaces/${workspaceId}/runs/${runId}/clarify`, { answer });
        setClarifiedAnswer(answer);
      } catch {
        // A failed submission keeps the card in its Awaiting state with the answer intact; the
        // run remains paused server-side either way.
      } finally {
        setSubmittingClarify(false);
      }
    },
    [workspaceId, runId],
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
          // The first run.status message of this connection is the server's replayed snapshot of the
          // state the viewer JOINED (a terminal run replays "complete"), not a transition — only a
          // completion observed AFTER joining arms the report auto-navigate. Refresh once for the
          // real transition to pick up the materialized report_id; load() gates the redirect on
          // completionObserved, so a terminal run mounted from elsewhere (report → "View source run")
          // never bounces back.
          if (runStatusSnapshotSeen.current) {
            if (status === RunStatus.Complete) {
              completionObserved.current = true;
              void load();
            }
          }
          runStatusSnapshotSeen.current = true;
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
    completionObserved.current = false;
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
        onOpen: () => {
          // Every (re)connection begins with the server replaying a run.status snapshot; reset the
          // per-connection snapshot guard so that snapshot is never mistaken for a transition.
          runStatusSnapshotSeen.current = false;
        },
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
  const forkedRuns = state.status === "loaded" ? state.forkedRuns : [];

  // Parse the step payloads at the boundary (schema.ts's convention on Step).
  const planStep = steps.find((step) => step.step_name === "plan");
  const plan = planStep ? parsePlanStep(planStep.output) : null;
  const generateSQLStep = steps.find((step) => step.step_name === "generate_sql");
  const generateSQL = generateSQLStep ? parseGenerateSQLStep(generateSQLStep.output) : null;
  const executeStep = steps.find((step) => step.step_name === "execute");
  const executeResult = executeStep ? parseExecuteStep(executeStep.output) : null;

  const runPill = run ? runStatusPill(run.status) : null;
  const question = run?.question_text ?? "";

  // FEAT-033's Clarification Needed state (screen 12): the run is paused (halted) with a pending
  // clarifying question in the plan output. Distinct from a user halt, which the plan output does
  // not carry.
  const needsClarification =
    run?.status === RunStatus.Halted &&
    plan !== null &&
    plan.needs_clarification === true &&
    !plan.clarification_answer;
  const clarificationResolved =
    plan?.needs_clarification === true && Boolean(plan.clarification_answer);
  const clarificationQuestion =
    plan?.clarification_question?.trim() || "This question needs a clarification before it can be answered.";

  // Phase 2 form of the Clarification Needed state: the plan step completed saying the question is
  // not answerable, so the run failed — surface the reason as a banner.
  const notAnswerable =
    run?.status === RunStatus.Failed &&
    plan !== null &&
    plan.answerable === false &&
    plan.needs_clarification !== true;
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

      {/* FEAT-025's branch/tree presentation: a forked run links to its parent; a run with
          branches lists them. Report history reads as a traceable tree, never a flat list. */}
      {run && isForkedRun(run) && run.parent_run_id && (
        <Link
          href={routes.run(workspaceId, run.parent_run_id)}
          className="mb-2 inline-flex items-center gap-1 text-body-small text-provenance hover:text-provenance"
        >
          <GitBranch size={14} aria-hidden="true" />
          Forked from run {run.parent_run_id.slice(0, 8)}…
        </Link>
      )}
      {run && forkedRuns.length > 0 && (
        <div className="mb-4 rounded-md border border-border bg-surface p-3">
          <p className="text-caption uppercase tracking-wide text-text-secondary">
            Forked runs ({forkedRuns.length})
          </p>
          <ul className="mt-2 flex flex-col gap-1">
            {forkedRuns.map((fork) => (
              <li key={fork.id}>
                <Link
                  href={routes.run(workspaceId, fork.id)}
                  className="inline-flex items-center gap-1 text-body-small text-text-primary hover:text-provenance"
                >
                  <GitBranch size={13} className="shrink-0 text-text-secondary" aria-hidden="true" />
                  <span className="truncate">{fork.question_text || fork.id}</span>
                  <span className="ml-1 shrink-0 text-text-secondary">
                    {runStatusPill(fork.status).label.toLowerCase()}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        </div>
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
          <p className="font-medium">This question can&apos;t be answered from the available data</p>
          <p className="mt-1 text-text-secondary">{clarificationReason}</p>
        </div>
      )}

      {/* minmax(0, …) tracks are load-bearing: a `1fr` track defaults to minmax(auto, 1fr), whose
          minimum is the content's min-content width — a long one-line generated SQL would blow the
          page out horizontally instead of letting the code block's own overflow-x-auto engage. */}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.5fr)]">
        <Card>
          <CardHeader
            title="Steps"
            description="Plan → generate SQL → execute. Verification: static check, shadow check, adversarial review."
          />
          {run ? <StepFeed steps={steps} /> : <Skeleton className="h-32" />}
        </Card>

        <div className="flex flex-col gap-6">
          {/* Screen 12: the Clarifying Question Prompt, inline on the Run Viewer. FEAT-033 pauses
              the run with a SPECIFIC question instead of guessing; answering resumes it. */}
          {needsClarification && (
            <ClarifyingQuestionCard
              question={clarificationQuestion}
              options={plan?.clarification_options ?? []}
              onSubmit={handleClarifySubmit}
              submitting={submittingClarify}
              resolvedAnswer={clarifiedAnswer ?? undefined}
            />
          )}

          {/* After a clarification, the card collapses to a compact summary even across reloads
              (the answer is persisted in the plan output — screen 12's Resolved state). */}
          {clarificationResolved && needsClarification === false && (
            <ClarifyingQuestionCard
              question={clarificationQuestion}
              options={plan?.clarification_options ?? []}
              onSubmit={handleClarifySubmit}
              resolvedAnswer={plan?.clarification_answer}
            />
          )}
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
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { StatusPill, runStatusPill } from "@/components/ui/StatusPill";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { CodeBlock } from "@/components/shared/CodeBlock";
import { StepFeed } from "./StepFeed";
import { ResultTable } from "./ResultTable";
import {
  parseExecuteStep,
  parseGenerateSQLStep,
  parsePlanStep,
} from "./step-payloads";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { RunStatus, isTerminalRunStatus, type Run, type Step } from "@/types/schema";

export interface RunViewerScreenProps {
  workspaceId: string;
  runId: string;
}

interface GetRunResponse {
  run: Run;
  steps: Step[];
}

/**
 * How often the Run Viewer re-fetches run + steps while the run is live.
 *
 * Not a server/config threshold (IMPLEMENTATION.md §5 rule 12 scopes those to server-enforced
 * behavior) — this is a client-only refresh cadence for the Phase 2 viewer. The Phase 2 boundary
 * ships polling instead of the live event channel screens/08-run-viewer.md describes, because the
 * stream is Phase 3 work; polling against GET /v1/workspaces/:id/runs/:runId keeps the same
 * "viewer attached to server-side state" contract.
 */
const RUN_POLL_INTERVAL_MS = 1_000;

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; run: Run; steps: Step[] }
  | { status: "error"; message: string };

/**
 * Screen 8: Run Viewer (screens/08-run-viewer.md) — FEAT-013's viewer, Phase 2 form.
 *
 * Renders a run's live state from the persisted run + steps: a status pill, the step feed on the
 * left, and the always-visible generated SQL plus the executed result set on the right. Polls until
 * the run reaches a terminal status, then stops.
 *
 * Phase 2 boundary (IMPLEMENTATION.md "Phase 2 boundary assumptions"): halt, amend, and "Resume from
 * here" (FEAT-022/024) and the Clarifying Question Prompt's interactive surface (FEAT-033) are later
 * phases — this viewer is watch-only. The one Phase 2 form of ambiguity handling is the failed-run
 * banner for an unanswerable plan, since the plan step COMPLETES with that verdict and the reason
 * would otherwise be invisible.
 */
export function RunViewerScreen({ workspaceId, runId }: RunViewerScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const stopPolling = useRef(false);
  // Whether at least one fetch has landed a loaded state, used so a transient poll failure keeps the
  // last-known state on screen instead of flashing the error state (the same pattern as
  // WorkspaceHomeScreen's lastKnownWorkspace ref).
  const loaded = useRef(false);

  const load = useCallback(async () => {
    try {
      const response = await api.get<GetRunResponse>(
        `/v1/workspaces/${workspaceId}/runs/${runId}`,
      );
      setState({ status: "loaded", run: response.run, steps: response.steps });
      loaded.current = true;
      if (isTerminalRunStatus(response.run.status)) {
        stopPolling.current = true;
      }
    } catch (error) {
      // A failed poll after a successful load is a transient hiccup between polls: keep the
      // last-known state on screen and let the next interval retry, rather than flashing the error
      // state or freezing the view.
      if (loaded.current) return;
      stopPolling.current = true;
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId, runId]);

  useEffect(() => {
    stopPolling.current = false;
    loaded.current = false;
    // Mount-triggered async fetch; see WorkspaceHomeScreen.tsx for why this eslint rule is
    // suppressed here.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();

    const interval = setInterval(() => {
      if (!stopPolling.current) void load();
    }, RUN_POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [load]);

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <ErrorState
          description={state.message}
          onRetry={() => {
            stopPolling.current = false;
            void load();
          }}
          onGoBack={() => router.push(routes.workspace(workspaceId))}
        />
      </main>
    );
  }

  const run = state.status === "loaded" ? state.run : null;
  const steps = state.status === "loaded" ? state.steps : [];

  // Parse the three Phase 2 step payloads at the boundary (schema.ts's convention on Step).
  const planStep = steps.find((step) => step.step_name === "plan");
  const plan = planStep ? parsePlanStep(planStep.output) : null;
  const generateSQLStep = steps.find((step) => step.step_name === "generate_sql");
  const generateSQL = generateSQLStep ? parseGenerateSQLStep(generateSQLStep.output) : null;
  const executeStep = steps.find((step) => step.step_name === "execute");
  const executeResult = executeStep ? parseExecuteStep(executeStep.output) : null;

  const runPill = run ? runStatusPill(run.status) : null;
  const question = run?.question_text ?? "";

  // Phase 2 form of the Clarification Needed state (screens/08): the plan step completed saying the
  // question is not answerable, so the run failed — surface the reason as a banner, since no step
  // row carries a failure message.
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
            <CodeBlock
              label="Generated SQL"
              code={generateSQL?.sql ?? ""}
              hint="View-only in Phase 2 — SQL editing arrives with amend (later phase)."
            />
          </Card>

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

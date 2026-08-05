"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { TypeReviewStep } from "./TypeReviewStep";
import { PIIReviewStep } from "./PIIReviewStep";
import { JoinReviewStep, type JoinDecision } from "./JoinReviewStep";
import {
  CONFIRMATION_STEPS,
  STEP_LABELS,
  type ConfirmationCandidateJoin,
  type ConfirmationColumn,
  type ConfirmationStep,
  type ListCandidateJoinsResponse,
  type ListColumnsResponse,
  type UpdateCandidateJoinResponse,
  type UpdateColumnResponse,
} from "./types";

export interface IngestConfirmationScreenProps {
  workspaceId: string;
}

type ScreenState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; columns: ConfirmationColumn[]; joins: ConfirmationCandidateJoin[] };

/**
 * Screen 7: Ingest Confirmation (screens/07-ingest-confirmation.md) — FEAT-006 (type review),
 * FEAT-007 (PII flag review), and FEAT-009 (candidate join accept/reject). Called out in the source
 * spec as THE HIGHEST-RISK UX IN THE PRODUCT: it is the mandatory review gate before anything
 * ingested is queryable, so every state and edge case here is load-bearing, not polish.
 *
 * Desktop-optimized (a dense `<table>` per step) but remains usable on mobile (a stacked list) — see
 * TypeReviewStep/PIIReviewStep/JoinReviewStep, each of which renders both and toggles via viewport.
 */
export function IngestConfirmationScreen({ workspaceId }: IngestConfirmationScreenProps) {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const [activeStep, setActiveStep] = useState<ConfirmationStep>("types");
  // Client-only marker: which steps the user chose "Use suggested defaults" for. Purely a visual
  // "reviewed" indicator — screens/07 is explicit that the underlying data stays fully open to
  // inspect/override afterward, so this never locks a step or hides its controls.
  const [defaultsApplied, setDefaultsApplied] = useState<Set<ConfirmationStep>>(new Set());
  const [savingIds, setSavingIds] = useState<Set<string>>(new Set());
  const [rowErrors, setRowErrors] = useState<Record<string, string>>({});
  const [submitting, setSubmitting] = useState(false);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const [columnsRes, joinsRes] = await Promise.all([
        api.get<ListColumnsResponse>(`/v1/workspaces/${workspaceId}/columns`),
        api.get<ListCandidateJoinsResponse>(`/v1/workspaces/${workspaceId}/candidate-joins`),
      ]);
      setState({
        status: "loaded",
        columns: columnsRes.columns ?? [],
        joins: joinsRes.candidate_joins ?? [],
      });
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  const datasetCount = useMemo(() => {
    if (state.status !== "loaded") return 0;
    return new Set(state.columns.map((c) => c.dataset_id)).size;
  }, [state]);

  // screens/07's edge case: "Single-file upload -> the Relationships step/tab is omitted entirely
  // rather than shown empty." Determined from the columns response's distinct dataset count, NOT
  // from whether any candidate joins exist — a genuine 2-file upload with zero surviving candidates
  // must still show this step (with its own empty state), while a single file must never show it.
  const steps = useMemo(
    () => CONFIRMATION_STEPS.filter((s) => s !== "relationships" || datasetCount >= 2),
    [datasetCount],
  );

  const columnsById = useMemo(() => {
    const map = new Map<string, ConfirmationColumn>();
    if (state.status === "loaded") {
      for (const c of state.columns) map.set(c.id, c);
    }
    return map;
  }, [state]);

  const stepIndex = steps.indexOf(activeStep);
  const isLastStep = stepIndex === steps.length - 1;

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

  async function handleChangeType(columnId: string, inferredType: string) {
    if (state.status !== "loaded") return;
    markSaving(columnId, true);
    clearRowError(columnId);
    try {
      const res = await api.patch<UpdateColumnResponse>(`/v1/workspaces/${workspaceId}/columns/${columnId}`, {
        inferred_type: inferredType,
      });
      setState((current) =>
        current.status === "loaded"
          ? {
              ...current,
              columns: current.columns.map((c) => (c.id === columnId ? { ...c, ...res.column } : c)),
            }
          : current,
      );
    } catch (error) {
      // Per screens/07's Error state: the user's in-progress selection is preserved (the select
      // reverts to its last-known-good value because we never mutated local state on failure), and
      // only this row shows the failure.
      const message = error instanceof ApiError ? error.message : "Could not save this change. Try again.";
      setRowError(columnId, message);
    } finally {
      markSaving(columnId, false);
    }
  }

  async function handleTogglePII(columnId: string, nextFlag: boolean) {
    if (state.status !== "loaded") return;
    markSaving(columnId, true);
    clearRowError(columnId);
    try {
      const res = await api.patch<UpdateColumnResponse>(`/v1/workspaces/${workspaceId}/columns/${columnId}`, {
        pii_flag: nextFlag,
      });
      setState((current) =>
        current.status === "loaded"
          ? {
              ...current,
              columns: current.columns.map((c) => (c.id === columnId ? { ...c, ...res.column } : c)),
            }
          : current,
      );
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not save this change. Try again.";
      setRowError(columnId, message);
    } finally {
      markSaving(columnId, false);
    }
  }

  async function handleDecideJoin(joinId: string, decision: JoinDecision) {
    if (state.status !== "loaded") return;
    markSaving(joinId, true);
    clearRowError(joinId);
    try {
      const res = await api.patch<UpdateCandidateJoinResponse>(
        `/v1/workspaces/${workspaceId}/candidate-joins/${joinId}`,
        { status: decision },
      );
      setState((current) =>
        current.status === "loaded"
          ? {
              ...current,
              joins: current.joins.map((j) => (j.id === joinId ? { ...j, ...res.candidate_join } : j)),
            }
          : current,
      );
    } catch (error) {
      const message = error instanceof ApiError ? error.message : "Could not save this decision. Try again.";
      setRowError(joinId, message);
    } finally {
      markSaving(joinId, false);
    }
  }

  // "Use suggested defaults" (types/PII steps only) applies the ALREADY-persisted inferred
  // types/PII flags — there is nothing to write, since the defaults are exactly what ingest already
  // stored. It only marks the step reviewed and advances. Candidate joins never get this shortcut:
  // per FEAT-009's non-negotiable requirement, every join needs an explicit accept/reject, so this
  // handler is never wired to the Relationships step at all (see the render below).
  function handleUseDefaults() {
    setDefaultsApplied((prev) => new Set(prev).add(activeStep));
    advanceOrFinish();
  }

  function advanceOrFinish() {
    if (isLastStep) {
      finish();
    } else {
      setActiveStep(steps[stepIndex + 1]);
    }
  }

  function finish() {
    setSubmitting(true);
    // No batch "confirm" call is needed: every override and every join decision was already
    // persisted individually as the user acted (see the handlers above) — Continue's only job here
    // is to navigate on to Workspace Home, per user_flow.md Flow 1 step 7.
    router.push(routes.workspace(workspaceId));
  }

  if (state.status === "loading") {
    return (
      <main id="main-content" className="mx-auto flex w-full max-w-5xl flex-col gap-6 p-8">
        <Card>
          <CardHeader title="Confirm your data" description="Loading what we found…" />
          <SkeletonList count={4} />
        </Card>
      </main>
    );
  }

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <ErrorState onRetry={() => void load()} />
      </main>
    );
  }

  return (
    <main id="main-content" className="mx-auto flex w-full max-w-5xl flex-col gap-6 p-8 max-md:pb-24">
      <div>
        <h1 className="text-h1 text-text-primary">Confirm your data</h1>
        <p className="mt-1 text-body-small text-text-secondary">
          Review inferred types, sensitive columns, and any relationships we found before you start
          asking questions.
        </p>
      </div>

      {/* Stepper/Tabs — screens/07's three-step structure. */}
      <div role="tablist" aria-label="Ingest confirmation steps" className="flex gap-1 border-b border-border">
        {steps.map((step, i) => {
          const isActive = step === activeStep;
          return (
            <button
              key={step}
              type="button"
              role="tab"
              aria-selected={isActive}
              onClick={() => setActiveStep(step)}
              className={
                "touch-target px-4 py-2 text-body font-medium " +
                (isActive
                  ? "border-b-2 border-primary text-primary"
                  : "text-text-secondary hover:text-text-primary")
              }
            >
              {i + 1}. {STEP_LABELS[step]}
              {defaultsApplied.has(step) && (
                <span className="ml-2 text-body-small text-success">✓ defaults applied</span>
              )}
            </button>
          );
        })}
      </div>

      <Card className="flex flex-col gap-4">
        {/*
         * The content clip/scroll region is WEB-ONLY (md:). On mobile there is no dense table to
         * scroll independently of the page — screens/07's mobile layout is a normal scrolling list
         * with a bottom action bar, the typical mobile pattern, not a clipped inner viewport. Forcing
         * the same max-h-[60vh] overflow-y-auto on mobile clipped a row's Accept/Reject controls
         * mid-card behind the action bar below — caught only by looking at an actual mobile
         * screenshot, exactly the failure mode IMPLEMENTATION.md §5 rule 13 warns about.
         */}
        <div className="md:max-h-[60vh] md:overflow-y-auto">
          {activeStep === "types" && (
            <TypeReviewStep
              columns={state.columns}
              savingIds={savingIds}
              errors={rowErrors}
              onChangeType={handleChangeType}
            />
          )}
          {activeStep === "pii" && (
            <PIIReviewStep
              columns={state.columns}
              savingIds={savingIds}
              errors={rowErrors}
              onToggleFlag={handleTogglePII}
            />
          )}
          {activeStep === "relationships" && (
            <JoinReviewStep
              joins={state.joins}
              columnsById={columnsById}
              savingIds={savingIds}
              errors={rowErrors}
              onDecide={handleDecideJoin}
            />
          )}
        </div>

        {/* Web: persistent action bar at the bottom of the CONTENT area (sticky within the
            md:overflow-y-auto region above), not the viewport — stays reachable while scrolling a
            long table, per screens/07's Web layout note. Mobile: the normal bottom action bar
            pattern, which "sticky" alone cannot express here (this div has no scrolling ancestor of
            its own on mobile, since the region above no longer clips/scrolls independently — see the
            comment on that div), so it is a fixed-to-viewport bar instead. */}
        <div className="flex items-center justify-between gap-2 border-t border-border bg-surface p-4 max-md:fixed max-md:inset-x-0 max-md:bottom-0 max-md:z-10 max-md:shadow-md md:sticky md:bottom-0 md:p-0 md:pt-4">
          {activeStep !== "relationships" ? (
            <Button variant="ghost" onClick={handleUseDefaults} disabled={submitting}>
              Use suggested defaults
            </Button>
          ) : (
            <span className="text-body-small text-text-secondary">
              Every relationship needs an explicit accept or reject.
            </span>
          )}
          <Button variant="primary" onClick={advanceOrFinish} loading={submitting} disabled={submitting}>
            {isLastStep ? "Continue" : "Next"}
          </Button>
        </div>
      </Card>
    </main>
  );
}

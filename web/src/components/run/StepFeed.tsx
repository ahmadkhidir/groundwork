import { AlertTriangle, CheckCircle2, Circle, Loader2, PauseCircle, ShieldAlert, ShieldCheck, XCircle } from "lucide-react";
import type { ReactNode } from "react";
import type { Step, StepStatus } from "@/types/schema";
import { STEP_NAMES } from "@/types/schema";
import {
  parseAdversarialReviewStep,
  parseShadowCheckStep,
  parseStaticCheckStep,
} from "@/components/run/step-payloads";

export interface StepFeedProps {
  steps: Step[];
}

/** Human label per step name, for the feed rows (STEP_NAMES is the source of order, not display). */
const STEP_LABELS: Partial<Record<(typeof STEP_NAMES)[number], string>> = {
  profile: "Profile",
  plan: "Plan",
  generate_sql: "Generate SQL",
  static_check: "Static check",
  execute: "Execute",
  reconcile: "Reconcile",
  shadow_check: "Shadow check",
  adversarial_review: "Adversarial review",
  chart_spec: "Chart spec",
  narrate: "Narrate",
  assemble: "Assemble",
};

function stepLabel(stepName: Step["step_name"]): string {
  return STEP_LABELS[stepName] ?? stepName;
}

function StatusIcon({ status }: { status: StepStatus }) {
  switch (status) {
    case "running":
      return <Loader2 size={16} className="animate-spin text-info" aria-hidden="true" />;
    case "complete":
      return <CheckCircle2 size={16} className="text-success" aria-hidden="true" />;
    case "failed":
      return <XCircle size={16} className="text-error" aria-hidden="true" />;
    case "halted":
      return <PauseCircle size={16} className="text-warning" aria-hidden="true" />;
    default:
      return <Circle size={16} className="text-text-secondary" aria-hidden="true" />;
  }
}

const STATUS_LABELS: Record<StepStatus, string> = {
  pending: "Pending",
  running: "Running",
  complete: "Complete",
  failed: "Failed",
  halted: "Halted",
};

/**
 * The live-event step list (screens/08-run-viewer.md): one row per DAG step the run has persisted,
 * in canonical pipeline order, with a status icon and label.
 *
 * Phase 2 boundary: only plan, generate_sql, and execute exist this wave — the linear executor never
 * writes static_check … assemble rows — so the feed renders exactly the persisted steps and states
 * the fact that later verification steps arrive in later phases rather than showing a permanent
 * "pending" row for a step that will never run.
 */
/**
 * Renders a completed static_check step's advisory findings (FEAT-028 AC2: surfaced, not buried).
 * Warnings are informational — they never block a run — so they read quieter than a failed step.
 */
function renderStaticCheckWarnings(step: Step): ReactNode | null {
  const payload = parseStaticCheckStep(step.output);
  if (payload === null || payload.warnings.length === 0) return null;
  return (
    <ul className="mt-1 flex flex-col gap-1">
      {payload.warnings.map((warning, i) => (
        <li
          key={`${warning.category}-${i}`}
          className="flex items-start gap-1.5 rounded-md bg-surface-secondary px-2 py-1.5"
        >
          <AlertTriangle size={14} className="mt-[3px] shrink-0 text-warning" aria-hidden="true" />
          <p className="text-body-small text-text-primary">{warning.message}</p>
        </li>
      ))}
    </ul>
  );
}

/**
 * Renders a completed shadow_check step's cross-checks (FEAT-031). Each check is shown honestly:
 * a matched total reads as a quiet confirmation, a MISMATCH is surfaced with warning styling —
 * never silently discarded (AC2). When the check was skipped (no aggregate, or an unparseable
 * shape), the skipped_reason explains why rather than leaving the row empty.
 */
function renderShadowCheck(step: Step): ReactNode | null {
  const payload = parseShadowCheckStep(step.output);
  if (payload === null) return null;
  if (payload.checks.length === 0) {
    if (!payload.skipped_reason) return null;
    return (
      <p className="mt-1 rounded-md bg-surface-secondary px-2 py-1.5 text-body-small text-text-secondary">
        {payload.skipped_reason}
      </p>
    );
  }
  return (
    <ul className="mt-1 flex flex-col gap-1">
      {payload.checks.map((check, i) => (
        <li
          key={`${check.column}-${i}`}
          className={`flex items-start gap-1.5 rounded-md px-2 py-1.5 ${
            check.matched ? "bg-surface-secondary" : "bg-warning/12"
          }`}
        >
          {check.matched ? (
            <CheckCircle2 size={14} className="mt-[3px] shrink-0 text-success" aria-hidden="true" />
          ) : (
            <AlertTriangle size={14} className="mt-[3px] shrink-0 text-warning" aria-hidden="true" />
          )}
          <p className="text-body-small text-text-primary">{check.message}</p>
        </li>
      ))}
    </ul>
  );
}

/**
 * Renders a completed adversarial_review step's verdict (FEAT-032). A passing review reads as a
 * quiet confirmation; a warning-severity mismatch is flagged with warning styling (it does not
 * block, so the viewer must still surface it). A blocking mismatch fails the step itself, whose
 * failed-output rendering already shows the verdict.
 */
function renderAdversarialReview(step: Step): ReactNode | null {
  const payload = parseAdversarialReviewStep(step.output);
  if (payload === null) return null;
  const flag = !payload.matches && payload.severity === "warning";
  return (
    <p
      className={`mt-1 flex items-start gap-1.5 rounded-md px-2 py-1.5 ${
        flag ? "bg-warning/12" : "bg-surface-secondary"
      }`}
    >
      {flag ? (
        <ShieldAlert size={14} className="mt-[3px] shrink-0 text-warning" aria-hidden="true" />
      ) : (
        <ShieldCheck size={14} className="mt-[3px] shrink-0 text-success" aria-hidden="true" />
      )}
      <span className={`text-body-small ${flag ? "text-text-primary" : "text-text-secondary"}`}>
        {payload.verdict}
      </span>
    </p>
  );
}

export function StepFeed({ steps }: StepFeedProps) {
  if (steps.length === 0) {
    return <p className="text-body-small text-text-secondary">Waiting for the run to begin…</p>;
  }

  const ordered = [...steps].sort(
    (a, b) => STEP_NAMES.indexOf(a.step_name) - STEP_NAMES.indexOf(b.step_name),
  );

  return (
    <ol className="flex flex-col gap-1" aria-label="Run steps">
      {ordered.map((step) => (
        <li
          key={step.id}
          className={`flex items-start gap-2 rounded-md p-2 ${
            step.status === "running" ? "bg-surface-secondary" : ""
          }`}
        >
          <span className="mt-[2px] shrink-0">
            <StatusIcon status={step.status} />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex items-baseline justify-between gap-2">
              <p className="text-body font-medium text-text-primary">
                {stepLabel(step.step_name)}
              </p>
              <p className="text-caption uppercase tracking-wide text-text-secondary">
                {STATUS_LABELS[step.status]}
              </p>
            </div>
            {step.status === "failed" && step.output ? (
              <p className="mt-1 text-body-small text-error">{step.output}</p>
            ) : null}
            {step.step_name === "static_check" && step.status === "complete"
              ? renderStaticCheckWarnings(step)
              : null}
            {step.step_name === "shadow_check" && step.status === "complete"
              ? renderShadowCheck(step)
              : null}
            {step.step_name === "adversarial_review" && step.status === "complete"
              ? renderAdversarialReview(step)
              : null}
          </div>
        </li>
      ))}
    </ol>
  );
}

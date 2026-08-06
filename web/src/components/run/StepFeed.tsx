import { CheckCircle2, Circle, Loader2, PauseCircle, XCircle } from "lucide-react";
import type { Step, StepStatus } from "@/types/schema";
import { STEP_NAMES } from "@/types/schema";

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
          </div>
        </li>
      ))}
    </ol>
  );
}

import { Check } from "lucide-react";
import { cn } from "@/lib/cn";
import type { IngestStep } from "./types";

interface StepDef {
  key: IngestStep;
  label: string;
}

/** The four steps screens/06-ingest-progress.md's layout describes, in order. */
const ALL_STEPS: StepDef[] = [
  { key: "provisioning", label: "Provisioning engine" },
  { key: "loading", label: "Loading data" },
  { key: "profiling", label: "Profiling columns" },
  { key: "relationships", label: "Finding relationships" },
];

export interface StepIndicatorProps {
  currentStep: IngestStep;
  /**
   * Whether to show "Finding relationships" — screens/06's layout lists it only "(if 2+ files)".
   * Failed is never passed here; the screen shows its own Ingest Error panel instead (see
   * IngestProgressScreen).
   */
  showRelationships: boolean;
}

/**
 * Screen 6's centered step indicator: "Provisioning engine" → "Loading data" → "Profiling columns"
 * → (if 2+ files) "Finding relationships". Completed steps show a checkmark; the active step is
 * highlighted with an indeterminate progress affordance — never a bare unlabeled spinner, per
 * branding.md's honest-status principle every label stays visible alongside it.
 */
export function StepIndicator({ currentStep, showRelationships }: StepIndicatorProps) {
  const steps = showRelationships
    ? ALL_STEPS
    : ALL_STEPS.filter((step) => step.key !== "relationships");
  const currentIndex = steps.findIndex((step) => step.key === currentStep);

  return (
    <ol className="flex flex-col gap-4" aria-label="Ingest progress">
      {steps.map((step, index) => {
        const isComplete = currentIndex >= 0 && index < currentIndex;
        const isActive = index === currentIndex;

        return (
          <li key={step.key} className="flex items-center gap-3">
            <span
              aria-hidden="true"
              className={cn(
                "flex size-6 shrink-0 items-center justify-center rounded-full border text-caption font-medium",
                isComplete && "border-success bg-success text-white",
                isActive && "border-primary bg-primary-light text-primary",
                !isComplete && !isActive &&
                  "border-border bg-surface-secondary text-text-secondary",
              )}
            >
              {isComplete ? <Check size={14} /> : index + 1}
            </span>

            <span
              className={cn(
                "text-body",
                isActive ? "font-medium text-text-primary" : "text-text-secondary",
              )}
            >
              {step.label}
              {isComplete && <span className="sr-only"> (complete)</span>}
              {isActive && <span className="sr-only"> (in progress)</span>}
            </span>

            {isActive && (
              <span
                role="progressbar"
                aria-label={`${step.label} in progress`}
                className="ml-auto h-1.5 w-20 shrink-0 overflow-hidden rounded-full bg-surface-secondary"
              >
                {/*
                 * Indeterminate: neither this screen nor the ingest pipeline currently has a
                 * byte-level sub-progress signal for "Loading data" (screens/06's edge case
                 * explicitly allows falling back to indeterminate when no genuine sub-progress is
                 * available), so this is the honest default rather than a fabricated determinate
                 * fill. `animate-pulse` is a built-in Tailwind utility — no new keyframes needed.
                 */}
                <span className="block h-full w-2/3 animate-pulse rounded-full bg-primary" />
              </span>
            )}
          </li>
        );
      })}
    </ol>
  );
}

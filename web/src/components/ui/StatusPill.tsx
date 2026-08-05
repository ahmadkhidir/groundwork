import type { ReactNode } from "react";
import { cn } from "@/lib/cn";
import { EngineStatus, RunStatus } from "@/types/schema";

/**
 * Status pill variants, one per row of branding.md's Status Pills table.
 *
 * Backgrounds are the semantic color at 12% opacity with full-strength text, which is why every
 * variant below uses the `/12` opacity modifier rather than a separate tint token.
 */
export type StatusVariant =
  | "neutral"
  | "info"
  | "success"
  | "warning"
  | "error";

const VARIANT_CLASSES: Record<StatusVariant, string> = {
  neutral: "bg-surface-secondary text-text-secondary",
  info: "bg-info/12 text-info",
  success: "bg-success/12 text-success",
  warning: "bg-warning/12 text-warning",
  error: "bg-error/12 text-error",
};

export interface StatusPillProps {
  variant: StatusVariant;
  /**
   * The visible text label. REQUIRED, not optional, and deliberately so.
   *
   * Status is never conveyed by color alone (branding.md §Color independence, and the product's
   * honest-status principle). A pill with no label would be a colored dot a colorblind user cannot
   * read, so the type system refuses to construct one.
   */
  children: ReactNode;
  /** Optional leading dot, e.g. the engine-status indicator. Decorative only — the label carries the meaning. */
  showDot?: boolean;
  className?: string;
}

/**
 * A small labelled status indicator used for engine state, run state, and reconciliation results.
 *
 * Surfacing system state honestly is a core product principle, so this component is intentionally
 * incapable of rendering color without text.
 */
export function StatusPill({
  variant,
  children,
  showDot = false,
  className,
}: StatusPillProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1",
        "rounded-sm px-2 py-[2px]",
        "text-caption font-medium uppercase tracking-wide",
        VARIANT_CLASSES[variant],
        className,
      )}
    >
      {showDot && (
        <span
          aria-hidden="true"
          className="size-[6px] rounded-full bg-current"
        />
      )}
      {children}
    </span>
  );
}

/**
 * Maps an engine status to its pill variant and human label.
 *
 * Centralized so `waking` reads identically everywhere it appears, rather than being re-mapped —
 * and re-worded — on each screen that shows it.
 */
export function engineStatusPill(status: EngineStatus): {
  variant: StatusVariant;
  label: string;
} {
  switch (status) {
    case EngineStatus.Sleeping:
      return { variant: "neutral", label: "Sleeping" };
    case EngineStatus.Waking:
      return { variant: "info", label: "Waking" };
    case EngineStatus.Ready:
      return { variant: "success", label: "Ready" };
  }
}

/** Maps a run status to its pill variant and human label. */
export function runStatusPill(status: RunStatus): {
  variant: StatusVariant;
  label: string;
} {
  switch (status) {
    case RunStatus.Queued:
      return { variant: "neutral", label: "Queued" };
    case RunStatus.Waking:
      return { variant: "info", label: "Waking" };
    case RunStatus.Running:
      return { variant: "info", label: "Running" };
    case RunStatus.Halted:
      return { variant: "warning", label: "Halted" };
    case RunStatus.Failed:
      return { variant: "error", label: "Failed" };
    case RunStatus.Complete:
      return { variant: "success", label: "Complete" };
  }
}

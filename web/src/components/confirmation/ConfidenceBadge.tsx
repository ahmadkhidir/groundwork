import { StatusPill, type StatusVariant } from "@/components/ui/StatusPill";
import type { ConfidenceTier } from "./types";

const TIER_VARIANT: Record<ConfidenceTier, StatusVariant> = {
  high: "success",
  medium: "warning",
  low: "error",
};

export interface ConfidenceBadgeProps {
  tier: ConfidenceTier;
  /**
   * The numeric percent OR basis text, shown alongside the tier label. REQUIRED — branding.md is
   * explicit that a confidence indicator is "always paired with the numeric % or basis text, never
   * color-only."
   */
  detail: string;
}

/**
 * The Confidence Badge component named in screens/07-ingest-confirmation.md's Components table:
 * visual confidence tier (high/medium/low) paired with its numeric or plain-language backing.
 */
export function ConfidenceBadge({ tier, detail }: ConfidenceBadgeProps) {
  return (
    <span className="inline-flex flex-wrap items-center gap-2">
      <StatusPill variant={TIER_VARIANT[tier]}>{tier}</StatusPill>
      <span className="text-body-small text-text-secondary">{detail}</span>
    </span>
  );
}

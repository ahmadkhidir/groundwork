import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Button } from "@/components/ui/Button";

export interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  subtitle?: ReactNode;
  ctaLabel?: string;
  onCta?: () => void;
}

/**
 * The shared Empty State pattern (screens.md's Shared/Common Components): centered icon, `h3`
 * title, `body-small` subtitle in `text-secondary`, primary-button CTA where applicable.
 *
 * Used across every screen with data fetching that can legitimately return nothing — Workspace
 * List (FEAT-002) and the Egress/Audit Log (FEAT-042) this wave, and every later screen listed in
 * screens.md's Shared/Common Components section.
 */
export function EmptyState({
  icon: Icon,
  title,
  subtitle,
  ctaLabel,
  onCta,
}: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center">
      <Icon size={40} className="text-text-secondary" aria-hidden="true" />
      <h3 className="text-h3 text-text-primary">{title}</h3>
      {subtitle && (
        <p className="max-w-sm text-body-small text-text-secondary">{subtitle}</p>
      )}
      {ctaLabel && onCta && (
        <Button variant="primary" onClick={onCta} className="mt-2">
          {ctaLabel}
        </Button>
      )}
    </div>
  );
}

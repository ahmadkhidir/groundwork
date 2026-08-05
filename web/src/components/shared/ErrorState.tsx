import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/Button";

export interface ErrorStateProps {
  /** Defaults to the shared copy: "Something went wrong." */
  title?: string;
  description?: string;
  onRetry: () => void;
  /** Optional secondary escape hatch, e.g. back to the workspace list. */
  onGoBack?: () => void;
}

/**
 * The shared Error State pattern (screens.md's Shared/Common Components): centered `error`-token
 * icon, `h3` title "Something went wrong," `body-small` description, primary "Try Again" (retries
 * the failed request), optional Ghost "Go Back".
 *
 * Used on every screen with data fetching per screens.md — Workspace List, File Upload, and the
 * Egress/Audit Log this wave.
 */
export function ErrorState({
  title = "Something went wrong",
  description = "We couldn't load this. Check your connection and try again.",
  onRetry,
  onGoBack,
}: ErrorStateProps) {
  return (
    <div className="flex flex-col items-center gap-2 px-4 py-12 text-center">
      <AlertTriangle size={40} className="text-error" aria-hidden="true" />
      <h3 className="text-h3 text-text-primary">{title}</h3>
      <p className="max-w-sm text-body-small text-text-secondary">{description}</p>
      <div className="mt-2 flex items-center gap-2">
        <Button variant="primary" onClick={onRetry}>
          Try Again
        </Button>
        {onGoBack && (
          <Button variant="ghost" onClick={onGoBack}>
            Go Back
          </Button>
        )}
      </div>
    </div>
  );
}

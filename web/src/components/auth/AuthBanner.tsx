import { AlertCircle, WifiOff } from "lucide-react";

export interface AuthBannerProps {
  message: string;
  /** `offline` swaps the icon; both variants use the same error-token styling — an offline banner
   * is still communicating a blocking condition, per branding.md's honesty-first status principle. */
  variant?: "error" | "offline";
}

/**
 * The inline error banner shared by every auth screen state: Auth Error, Offline, and any
 * unexpected failure. `role="alert"` so assistive technology announces it the moment it appears,
 * matching Input's own error handling.
 */
export function AuthBanner({ message, variant = "error" }: AuthBannerProps) {
  const Icon = variant === "offline" ? WifiOff : AlertCircle;

  return (
    <div
      role="alert"
      className="flex items-center gap-sm rounded-md border border-error bg-error/10 px-sm py-sm text-body-small text-error"
    >
      <Icon size={18} aria-hidden="true" className="shrink-0" />
      <span>{message}</span>
    </div>
  );
}

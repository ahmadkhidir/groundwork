"use client";

import { useState } from "react";
import { X } from "lucide-react";

/**
 * FEAT-052's post-deletion confirmation banner (screens/01): shown on the Landing Page after an
 * account deletion redirects with `?deleted=1`. Neutral, dismissible — "your account was deleted"
 * is the honest outcome, not a marketing message.
 */
export function DeletedAccountBanner() {
  const [dismissed, setDismissed] = useState(false);
  if (dismissed) return null;

  return (
    <div
      role="status"
      className="mx-auto flex w-full max-w-6xl items-center justify-between gap-3 border-b border-border bg-surface-secondary px-4 py-3"
    >
      <p className="text-body-small text-text-primary">
        Your account has been deleted. All of your data is gone.
      </p>
      <button
        type="button"
        aria-label="Dismiss"
        onClick={() => setDismissed(true)}
        className="shrink-0 rounded p-1 text-text-secondary hover:text-text-primary focus:outline-2 focus:outline-primary"
      >
        <X size={16} aria-hidden="true" />
      </button>
    </div>
  );
}

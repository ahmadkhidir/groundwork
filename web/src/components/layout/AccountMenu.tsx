"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import { CreditCard, UserRound } from "lucide-react";
import { routes } from "@/lib/routes";

export interface AccountMenuProps {
  /** Display name for the avatar initials; falls back to the email's first letter. */
  displayName: string;
  email: string;
}

/** Two-letter initials from a display name (or the email local-part), for the avatar. */
function initials(displayName: string, email: string): string {
  const source = displayName.trim() || email.trim();
  const parts = source.split(/\s+/);
  if (parts.length >= 2) {
    return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase();
  }
  return source.slice(0, 2).toUpperCase();
}

/**
 * The Navigation Shell's account menu (screens.md): the account-level entry point on every
 * authenticated screen. Items are "Plan & Billing" and "Profile & Security" — sign out is
 * DELIBERATELY not a menu item (FEAT-052: the single sign-out affordance lives only on Profile &
 * Security; the menu exists to navigate to where the action lives).
 */
export function AccountMenu({ displayName, email }: AccountMenuProps) {
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function handlePointerDown(event: MouseEvent) {
      if (!containerRef.current?.contains(event.target as Node)) setOpen(false);
    }
    function handleKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", handlePointerDown);
    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("mousedown", handlePointerDown);
      document.removeEventListener("keydown", handleKeyDown);
    };
  }, [open]);

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Account menu (${email})`}
        onClick={() => setOpen((v) => !v)}
        className="touch-target inline-flex h-(--size-button-height) w-(--size-button-height) items-center justify-center rounded-full border border-border bg-surface-secondary text-body-small font-semibold text-text-primary hover:border-primary focus:outline-2 focus:outline-primary"
      >
        {initials(displayName, email)}
      </button>

      {open && (
        <div
          role="menu"
          aria-label="Account"
          className="absolute right-0 top-full z-50 mt-1 w-56 overflow-hidden rounded-md border border-border bg-surface shadow-md"
        >
          <div className="border-b border-border px-3 py-2">
            <p className="truncate text-body-small font-medium text-text-primary">{displayName || email}</p>
            <p className="truncate text-caption text-text-secondary">{email}</p>
          </div>
          <Link
            role="menuitem"
            href={routes.accountSettings()}
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 px-3 py-2 text-body-small text-text-primary hover:bg-surface-secondary"
          >
            <CreditCard size={15} aria-hidden="true" />
            Plan &amp; Billing
          </Link>
          <Link
            role="menuitem"
            href={routes.settingsProfile()}
            onClick={() => setOpen(false)}
            className="flex items-center gap-2 px-3 py-2 text-body-small text-text-primary hover:bg-surface-secondary"
          >
            <UserRound size={15} aria-hidden="true" />
            Profile &amp; Security
          </Link>
        </div>
      )}
    </div>
  );
}

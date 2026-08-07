"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { Bell } from "lucide-react";
import { api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { UnreadCountResponse } from "./types";

/**
 * How often the bell re-polls the unread count while mounted. A client-only refresh cadence — the
 * same kind of fixed implementation detail as WorkspaceHomeScreen's engine-status poll; not a
 * `server/internal/config` threshold (those gate server-enforced product behavior).
 */
const UNREAD_POLL_INTERVAL_MS = 60_000;

/**
 * The navigation-shell notifications bell (screens/16 Components: "Notification bell | IconButton
 * (with unread badge)"), linking to the full Notifications page. Polls the lightweight
 * `GET /v1/notifications?unread_count=true` variant on an interval so the badge stays fresh on
 * every authenticated screen without a push channel.
 *
 * The bell is a standalone component because the full Navigation Shell (screens.md) has no home in
 * the codebase yet — the orchestrator mounts it where the shell eventually lands (see the
 * assembly report). It never breaks the shell it lives in: a fetch failure, or the Free-tier 403
 * that is the documented FEAT-026 gate, simply hides the badge until the next poll.
 */
export function NotificationsBell() {
  const [unreadCount, setUnreadCount] = useState(0);

  const load = useCallback(async () => {
    try {
      const response = await api.get<UnreadCountResponse>("/v1/notifications?unread_count=true");
      setUnreadCount(response.unread_count);
    } catch {
      setUnreadCount(0);
    }
  }, []);

  useEffect(() => {
    // Fetch on mount, then on an interval — see WorkspaceListScreen.tsx for why this eslint rule
    // is suppressed for a mount-triggered async fetch.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
    const interval = setInterval(() => void load(), UNREAD_POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [load]);

  return (
    <Link
      href={routes.notifications()}
      aria-label={
        unreadCount > 0 ? `Notifications (${unreadCount} unread)` : "Notifications"
      }
      className="touch-target relative inline-flex h-(--size-button-height) w-(--size-button-height) items-center justify-center rounded-md text-text-secondary hover:text-text-primary"
    >
      <Bell size={20} aria-hidden="true" />
      {unreadCount > 0 && (
        <span
          aria-hidden="true"
          className="absolute right-0 top-0 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-caption font-semibold text-white"
        >
          {unreadCount > 99 ? "99+" : unreadCount}
        </span>
      )}
    </Link>
  );
}

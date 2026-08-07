"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { BellOff } from "lucide-react";
import { Button, buttonClassNames } from "@/components/ui/Button";
import { EmptyState } from "@/components/shared/EmptyState";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { NotificationType } from "@/types/schema";
import { NotificationRow } from "./NotificationRow";
import type { NotificationWithLink, NotificationsListResponse } from "./types";

type ScreenState =
  | { status: "loading" }
  | {
      status: "loaded";
      notifications: NotificationWithLink[];
      unreadCount: number;
      nextCursor: string;
      loadingMore: boolean;
    }
  | { status: "error" }
  | { status: "upgrade" };

/**
 * Resolves a notification's polymorphic related entity into the deep link screens/16 specifies:
 * export ready → Report View, engine slept → Workspace Home, ingest failed → File Upload,
 * workspace invite → Workspace List. Returns null when the linked entity has been deleted — the
 * row stays (it is a historical record) but its link is disabled.
 *
 * The workspace id comes from the server (GET /v1/notifications resolves it), never guessed here:
 * a report or dataset id carries no workspace, and a revoked invite must land somewhere harmless.
 */
export function notificationHref(n: NotificationWithLink): string | null {
  switch (n.type) {
    case NotificationType.ExportReady:
      return n.workspace_id ? routes.report(n.workspace_id, n.related_entity_id) : null;
    case NotificationType.EngineSlept:
      return n.workspace_id ? routes.workspace(n.workspace_id) : null;
    case NotificationType.IngestFailed:
      return n.workspace_id ? routes.upload(n.workspace_id) : null;
    case NotificationType.WorkspaceInvite:
      // A revoked invite simply won't be on the workspace list — no broken navigation, and no
      // generic error, which is screens/16's "This invite is no longer valid" edge case in its
      // most honest form.
      return routes.workspaces();
  }
}

/**
 * Screen 16: Notifications (screens/16-notifications.md) — FEAT-026.
 *
 * A fetched list of durable notifications — export ready, engine slept, ingest failed, workspace
 * invite — deliberately separate from the high-frequency live run event stream (architecture.md).
 * It is NOT a live stream: the list is fetched on mount and refreshed after each acknowledge, and
 * "Load more" appends the next page of history (FEAT-026 AC3).
 */
export function NotificationsScreen() {
  const [state, setState] = useState<ScreenState>({ status: "loading" });

  const load = useCallback(async (silent = false) => {
    if (!silent) {
      setState({ status: "loading" });
    }
    try {
      const response = await api.get<NotificationsListResponse>("/v1/notifications?limit=50");
      setState({
        status: "loaded",
        notifications: response.notifications ?? [],
        unreadCount: response.unread_count ?? 0,
        nextCursor: response.next_cursor ?? "",
        loadingMore: false,
      });
    } catch (error) {
      if (error instanceof ApiError && error.isForbidden) {
        // monetization.md row 109: FEAT-026 is Pro/Team. The server is authoritative — show the
        // upgrade state rather than a generic error.
        setState({ status: "upgrade" });
        return;
      }
      if (silent) {
        // A background refresh after an acknowledge failed: keep the last-known list rather than
        // blanking an otherwise-working screen. The next explicit action or fetch surfaces the
        // server's real state.
        return;
      }
      setState({ status: "error" });
    }
  }, []);

  useEffect(() => {
    // Fetch on mount — see AuditLogScreen.tsx for why this eslint rule is suppressed for a
    // mount-triggered async fetch rather than a synchronous in-effect setState.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  async function acknowledge(id: string) {
    try {
      await api.patch(`/v1/notifications/${encodeURIComponent(id)}`, { read: true });
    } finally {
      // Re-fetch so the list and unread count reflect the server's authoritative state — the
      // screen is a fetched list that refreshes on acknowledge, never a client-optimistic stream.
      await load(true);
    }
  }

  async function acknowledgeAll() {
    try {
      await api.post("/v1/notifications/read-all");
    } finally {
      await load(true);
    }
  }

  async function loadMore() {
    if (state.status !== "loaded" || !state.nextCursor) {
      return;
    }
    setState({ ...state, loadingMore: true });
    try {
      const response = await api.get<NotificationsListResponse>(
        `/v1/notifications?limit=50&cursor=${encodeURIComponent(state.nextCursor)}`,
      );
      setState((current) =>
        current.status === "loaded"
          ? {
              status: "loaded",
              notifications: [...current.notifications, ...(response.notifications ?? [])],
              unreadCount: response.unread_count ?? current.unreadCount,
              nextCursor: response.next_cursor ?? "",
              loadingMore: false,
            }
          : current,
      );
    } catch {
      // "Load more" failing leaves the existing list intact rather than replacing it with an error
      // state — only the initial fetch does that.
      setState((current) =>
        current.status === "loaded" ? { ...current, loadingMore: false } : current,
      );
    }
  }

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <ErrorState onRetry={() => void load()} />
      </main>
    );
  }

  if (state.status === "upgrade") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
        <h1 className="text-h1 text-text-primary">Notifications</h1>
        <div className="mt-6 flex flex-col items-center gap-2 rounded-md border border-border bg-surface p-8 text-center shadow-sm">
          <BellOff size={40} className="text-text-secondary" aria-hidden="true" />
          <h3 className="text-h3 text-text-primary">Notifications are a Pro and Team feature</h3>
          <p className="max-w-sm text-body-small text-text-secondary">
            Durable notifications — export ready, engine slept, ingest failed — keep you informed
            when something finishes outside the current view.
          </p>
          <Link
            href={routes.accountSettings()}
            className={buttonClassNames("primary", "mt-2")}
          >
            Manage your plan
          </Link>
        </div>
      </main>
    );
  }

  const loaded = state.status === "loaded";

  return (
    <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-h1 text-text-primary">Notifications</h1>
          <p className="mt-1 text-body-small text-text-secondary">
            Exports that finished, engines that went to sleep, and other events from outside the
            current view.
          </p>
        </div>
        {loaded && state.unreadCount > 0 && (
          <Button variant="ghost" onClick={() => void acknowledgeAll()}>
            Mark all as read
          </Button>
        )}
      </div>

      <div className="flex flex-col gap-2">
        {state.status === "loading" && <SkeletonList count={4} />}

        {loaded && state.notifications.length === 0 && (
          <EmptyState
            icon={BellOff}
            title="No notifications yet"
            subtitle="Exports, engine sleep, and other durable events will appear here."
          />
        )}

        {loaded && state.notifications.length > 0 && (
          <>
            <ul className="flex flex-col gap-2">
              {state.notifications.map((n) => (
                <li key={n.id}>
                  <NotificationRow
                    notification={n}
                    href={notificationHref(n)}
                    onAcknowledge={(id) => void acknowledge(id)}
                  />
                </li>
              ))}
            </ul>

            {state.nextCursor && (
              <div className="mt-4 flex justify-center">
                <Button variant="ghost" onClick={() => void loadMore()} loading={state.loadingMore}>
                  Load more
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </main>
  );
}

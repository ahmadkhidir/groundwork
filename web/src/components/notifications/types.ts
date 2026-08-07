import type { Notification } from "@/types/schema";

/**
 * The wire shape GET /v1/notifications returns: a Notification plus the server-resolved
 * `workspace_id` the client needs to build a deep link (a report or dataset id carries no
 * workspace). `workspace_id` is absent when the linked entity has been deleted since the
 * notification was created — the row remains (it is a historical record) but the link is disabled,
 * per screens/16-notifications.md's edge case.
 */
export interface NotificationWithLink extends Notification {
  workspace_id?: string;
}

/** The full list response: one page of notifications plus the bell badge count. */
export interface NotificationsListResponse {
  notifications: NotificationWithLink[];
  unread_count: number;
  next_cursor: string;
}

/** The lightweight `?unread_count=true` variant the navigation-shell bell polls. */
export interface UnreadCountResponse {
  unread_count: number;
}

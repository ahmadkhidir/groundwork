"use client";

import Link from "next/link";
import {
  AlertTriangle,
  Check,
  FileDown,
  Moon,
  Users,
  type LucideIcon,
} from "lucide-react";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { cn } from "@/lib/cn";
import { NotificationType, type NotificationType as NotificationTypeValue } from "@/types/schema";
import { formatRelativeTime } from "./format";
import type { NotificationWithLink } from "./types";

/** One icon per durable notification type, so the row is scannable without reading the message. */
const NOTIFICATION_ICONS: Record<NotificationTypeValue, LucideIcon> = {
  [NotificationType.ExportReady]: FileDown,
  [NotificationType.EngineSlept]: Moon,
  [NotificationType.IngestFailed]: AlertTriangle,
  [NotificationType.WorkspaceInvite]: Users,
};

/** Human label for each notification type (screens/16: "Shows notification type, message…"). */
const NOTIFICATION_LABELS: Record<NotificationTypeValue, string> = {
  [NotificationType.ExportReady]: "Export ready",
  [NotificationType.EngineSlept]: "Engine asleep",
  [NotificationType.IngestFailed]: "Ingest failed",
  [NotificationType.WorkspaceInvite]: "Workspace invite",
};

export interface NotificationRowProps {
  notification: NotificationWithLink;
  /**
   * Resolved deep link for this notification, or null when the linked entity has been deleted —
   * the row then renders as a historical record with a tooltip and no navigation (screens/16 edge
   * case), never a broken link.
   */
  href: string | null;
  /** Marks the notification read. Called on the explicit button AND on row-open (auto-on-open). */
  onAcknowledge: (id: string) => void;
}

/**
 * One durable notification row: type icon + label, message, relative timestamp, read state, and an
 * acknowledge action. Unread rows are distinguished by a `primary` left accent bar and a bolder
 * message (screens/16 Layout: "unread ones visually distinguished (left accent bar in primary
 * token, bold title)").
 *
 * Clicking a linked row acknowledges it (if unread) and navigates — screens/16 Data Flow step 3
 * ("Clicking a notification marks it read and navigates to its linked screen") — while the explicit
 * "Mark as read" button acknowledges without navigating.
 */
export function NotificationRow({
  notification: n,
  href,
  onAcknowledge,
}: NotificationRowProps) {
  const Icon = NOTIFICATION_ICONS[n.type] ?? AlertTriangle;

  const content = (
    <>
      <Icon size={20} className="mt-0.5 shrink-0 text-text-secondary" aria-hidden="true" />
      <span className="min-w-0 flex-1">
        <span className="flex items-baseline gap-2">
          <span
            className={cn(
              "text-caption uppercase tracking-wide",
              n.read ? "text-text-secondary" : "font-semibold text-primary",
            )}
          >
            {NOTIFICATION_LABELS[n.type]}
          </span>
          <span className="text-body-small text-text-secondary">
            {formatRelativeTime(n.created_at)}
          </span>
        </span>
        <span
          className={cn(
            "mt-0.5 block text-body",
            n.read ? "text-text-secondary" : "font-medium text-text-primary",
          )}
        >
          {n.message}
        </span>
      </span>
    </>
  );

  return (
    <Card
      className={cn(
        "flex items-start gap-3 p-4",
        // The unread accent is a dedicated bar rather than a border override: it cannot fight the
        // Card's own `border-border` color in the CSS cascade, and its color is the `primary`
        // token per screens/16 ("left accent bar in primary token").
        !n.read && "relative",
      )}
    >
      {!n.read && (
        <span
          aria-hidden="true"
          className="absolute inset-y-0 left-0 w-1 rounded-l-md bg-primary"
        />
      )}
      {href ? (
        <Link
          href={href}
          onClick={() => {
            if (!n.read) {
              onAcknowledge(n.id);
            }
          }}
          className="flex min-w-0 flex-1 items-start gap-3 text-left"
        >
          {content}
        </Link>
      ) : (
        <div
          aria-disabled="true"
          title="This linked item is no longer available."
          className="flex min-w-0 flex-1 items-start gap-3 text-left opacity-70"
        >
          {content}
        </div>
      )}

      {!n.read && (
        <Button
          variant="ghost"
          touchTarget
          aria-label={`Mark "${n.message}" as read`}
          onClick={() => onAcknowledge(n.id)}
          className="shrink-0 px-2"
        >
          <Check size={16} aria-hidden="true" />
        </Button>
      )}
    </Card>
  );
}

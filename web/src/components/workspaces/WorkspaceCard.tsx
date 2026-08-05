import Link from "next/link";
import { Card } from "@/components/ui/Card";
import { StatusPill, engineStatusPill } from "@/components/ui/StatusPill";
import { routes } from "@/lib/routes";
import type { Workspace } from "@/types/schema";

export interface WorkspaceCardProps {
  workspace: Workspace;
  /** True when the current user has read-only (not owner) access — FEAT-045, not yet built, but the
   * badge is cheap to wire now so a later wave only has to supply the boolean. */
  sharedReadOnly?: boolean;
}

/**
 * One workspace card in the grid/list — screens/03-workspace-list.md's Workspace card component:
 * name, engine status pill, last-activity timestamp, and (once FEAT-045 lands) a shared badge.
 *
 * A very long name truncates with an ellipsis and carries the full name in `title`, satisfying the
 * screen's "very long name" edge case on both web (tooltip via `title`) and mobile (native
 * long-press/tap title behavior).
 */
export function WorkspaceCard({ workspace, sharedReadOnly = false }: WorkspaceCardProps) {
  const { variant, label } = engineStatusPill(workspace.engine_status);
  const lastActivity = new Date(workspace.updated_at);

  return (
    <Link href={routes.workspace(workspace.id)} className="block">
      <Card className="transition-colors hover:border-primary">
        <div className="flex items-start justify-between gap-2">
          <h3 title={workspace.name} className="truncate text-h3 text-text-primary">
            {workspace.name}
          </h3>
          {sharedReadOnly && (
            <StatusPill variant="neutral" className="shrink-0">
              Shared
            </StatusPill>
          )}
        </div>

        <div className="mt-3 flex items-center justify-between gap-2">
          <StatusPill variant={variant} showDot>
            {label}
          </StatusPill>
          <time
            dateTime={workspace.updated_at}
            className="text-caption text-text-secondary"
          >
            {lastActivity.toLocaleDateString(undefined, {
              month: "short",
              day: "numeric",
            })}
          </time>
        </div>
      </Card>
    </Link>
  );
}

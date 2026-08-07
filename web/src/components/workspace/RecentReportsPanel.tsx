"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { FileText } from "lucide-react";
import { Card } from "@/components/ui/Card";
import { Skeleton } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";

export interface RecentReportsPanelProps {
  workspaceId: string;
}

/**
 * Slim report summary served by GET /v1/workspaces/:id/reports — enough for the Workspace Home
 * card (title, timestamp) and its Report View link. The full self-contained Report payload stays on
 * the read endpoint; the list deliberately does not carry narrative/chart_spec (server:
 * `publicReport` in internal/api/reports.go).
 */
interface ReportSummary {
  id: string;
  run_id: string;
  title: string;
  created_at: string;
}

interface ListReportsResponse {
  reports: ReportSummary[];
}

type PanelState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "loaded"; reports: ReportSummary[] };

/** Renders a report's created_at as a compact "Aug 6, 2:00 PM"-style timestamp, like DatasetsPanel. */
function formatReportCreatedAt(iso: string): string {
  try {
    return new Date(iso).toLocaleString(undefined, {
      month: "short",
      day: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  } catch {
    return iso;
  }
}

/**
 * screens/04-workspace-home.md's "recent reports" section — FEAT-034. Fetches the workspace's most
 * recent reports (`reports?limit=10`, created_at DESC server-side) and renders each as a card row:
 * title + timestamp, linking to Report View (routes.report).
 *
 * States per screens/04: Loading (skeleton rows), Empty ("Your reports will appear here." — shown
 * when the workspace has data but no questions have been asked yet), Loaded (the list), and a
 * panel-scoped Error message. The error is deliberately inline (like DatasetsPanel) rather than the
 * full shared Error State: this is a secondary panel on a screen whose primary job does not depend
 * on it, and a second full-screen error block would mask an otherwise-working Workspace Home.
 */
export function RecentReportsPanel({ workspaceId }: RecentReportsPanelProps) {
  const [state, setState] = useState<PanelState>({ status: "loading" });

  const load = useCallback(async () => {
    try {
      const res = await api.get<ListReportsResponse>(
        `/v1/workspaces/${workspaceId}/reports?limit=10`,
      );
      setState({ status: "loaded", reports: res.reports ?? [] });
    } catch (error) {
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  if (state.status === "loading") {
    return (
      <Card>
        <div className="flex flex-col gap-2">
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
          <Skeleton className="h-12 w-full" />
        </div>
      </Card>
    );
  }

  if (state.status === "error") {
    return (
      <Card>
        <p className="text-body-small text-error">{state.message}</p>
      </Card>
    );
  }

  if (state.reports.length === 0) {
    return (
      <div className="flex flex-col items-center gap-2 rounded-md border border-dashed border-border px-4 py-10 text-center">
        <FileText size={32} className="text-text-secondary" aria-hidden="true" />
        <p className="text-body-small text-text-secondary">
          Your reports will appear here.
        </p>
      </div>
    );
  }

  return (
    <Card>
      <ul className="flex flex-col gap-1">
        {state.reports.map((report) => (
          <li
            key={report.id}
            className="rounded-md px-2 py-2 hover:bg-surface-secondary"
          >
            <Link href={routes.report(workspaceId, report.id)} className="block min-w-0">
              <p className="truncate text-body-small font-medium text-text-primary">
                {report.title}
              </p>
              <p className="text-caption text-text-secondary">
                {formatReportCreatedAt(report.created_at)}
              </p>
            </Link>
          </li>
        ))}
      </ul>
    </Card>
  );
}

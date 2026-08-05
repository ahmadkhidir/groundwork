"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { UploadCloud } from "lucide-react";
import { Card } from "@/components/ui/Card";
import { StatusPill, engineStatusPill } from "@/components/ui/StatusPill";
import { buttonClassNames } from "@/components/ui/Button";
import { ErrorState } from "@/components/shared/ErrorState";
import { Skeleton } from "@/components/shared/Skeleton";
import { AskPanel } from "./AskPanel";
import { RecentReportsPanel } from "./RecentReportsPanel";
import { SemanticLayerSummaryPanel } from "./SemanticLayerSummaryPanel";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { Workspace } from "@/types/schema";

export interface WorkspaceHomeScreenProps {
  workspaceId: string;
}

interface GetWorkspaceResponse {
  workspace: Workspace;
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; workspace: Workspace }
  | { status: "error"; message: string };

/**
 * How often the Engine Status Pill re-fetches live status while this screen is mounted.
 *
 * Not a `server/internal/config` threshold (IMPLEMENTATION.md §5 rule 12 scopes those to
 * server-enforced product behavior, e.g. the engine idle-teardown window itself) — this is a
 * client-only refresh cadence, the same kind of fixed implementation detail
 * `ingestStreamPollInterval` is on the Go side for the ingest stream.
 */
const ENGINE_STATUS_POLL_INTERVAL_MS = 10_000;

/**
 * Screen 4: Workspace Home (screens/04-workspace-home.md) — FEAT-011 this wave.
 *
 * FEAT-011's job is the engine sleep/wake lifecycle and its honest status display: the Engine Status
 * Pill here is always visible and reflects the LIVE value from GET /v1/workspaces/:id (which merges
 * in engine.Provider.Status server-side — see server/internal/api/engine.go), polled on an interval
 * rather than fetched once, since the engine's state can change in the background at any time.
 *
 * The rest of the screen's surface — the Ask panel (FEAT-013), recent reports (FEAT-034), and the
 * semantic layer summary (FEAT-019) — is laid out per spec but intentionally inert this wave. Each is
 * built as its own component, visibly captioned with the feature that owns making it real, rather
 * than silently accepting input or displaying invented data. See screens/04-workspace-home.md's Scope
 * discipline note for why: this screen's shell ships now so navigation and the honest engine status
 * are usable today, without pretending the rest of the product already exists.
 */
export function WorkspaceHomeScreen({ workspaceId }: WorkspaceHomeScreenProps) {
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  // Preserves the last-known engine status across a poll that fails or is still in flight, per
  // screens/04's Loading state: "engine status pill shows last-known state... rather than a neutral
  // skeleton" once at least one successful fetch has happened.
  const lastKnownWorkspace = useRef<Workspace | null>(null);

  const load = useCallback(async () => {
    try {
      const response = await api.get<GetWorkspaceResponse>(
        `/v1/workspaces/${workspaceId}`,
      );
      lastKnownWorkspace.current = response.workspace;
      setState({ status: "loaded", workspace: response.workspace });
    } catch (error) {
      // A failed poll after a successful initial load should not blank out an otherwise-working
      // screen — keep showing the last-known state rather than flashing an error on a transient
      // network hiccup between polls.
      if (lastKnownWorkspace.current) {
        return;
      }
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, [workspaceId]);

  useEffect(() => {
    // Fetching on mount, then on an interval, per screens/04-workspace-home.md's Data Flow ("engine
    // status shown immediately from cached/last-known value while a fresh fetch confirms it") — see
    // WorkspaceListScreen.tsx for why this eslint rule is suppressed for a mount-triggered async
    // fetch rather than a synchronous in-effect setState.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();

    const interval = setInterval(() => void load(), ENGINE_STATUS_POLL_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [load]);

  if (state.status === "error") {
    return (
      <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
        <ErrorState onRetry={() => void load()} />
      </main>
    );
  }

  const workspace = state.status === "loaded" ? state.workspace : null;
  const enginePill = workspace ? engineStatusPill(workspace.engine_status) : null;

  return (
    <main id="main-content" className="mx-auto w-full max-w-5xl p-8">
      {/* Minimal workspace-scoped navigation. screens.md's full Navigation Shell (Navbar + collapsible
          Sidebar) is a foundation-level, cross-screen concern with no home in the codebase yet —
          reported as a needed shared addition rather than built ad hoc here for just this screen. */}
      <nav
        aria-label="Workspace navigation"
        className="mb-4 hidden items-center gap-4 text-body-small lg:flex"
      >
        <span className="font-medium text-text-primary">Home</span>
        <Link
          href={routes.semanticLayer(workspaceId)}
          className="text-text-secondary hover:text-text-primary"
        >
          Semantic Layer
        </Link>
        <Link
          href={routes.settings(workspaceId)}
          className="text-text-secondary hover:text-text-primary"
        >
          Settings
        </Link>
      </nav>

      <div className="mb-6 flex items-center justify-between gap-4">
        {workspace ? (
          <h1 className="truncate text-h1 text-text-primary">{workspace.name}</h1>
        ) : (
          <Skeleton className="h-8 w-48" />
        )}

        {/* Engine Status Pill: always visible, per branding.md's honest-status principle — never
            replaced by a generic spinner, even while the very first fetch is still in flight. */}
        {enginePill ? (
          <StatusPill variant={enginePill.variant} showDot>
            {enginePill.label}
          </StatusPill>
        ) : (
          <StatusPill variant="neutral">Checking status…</StatusPill>
        )}
      </div>

      <Card className="mb-6">
        <AskPanel />
      </Card>

      {/* Upload CTA is always offered as a valid entry point to File Upload (FEAT-003/004, already
          shipped). Whether to HIDE it once a workspace already has ingested data — per
          screens/04-workspace-home.md's "Upload data prompt... shown when no files ingested yet" —
          depends on a dataset-presence check this screen has no endpoint for yet; reported as a gap
          for whichever wave wires FEAT-034's reports list (the natural place to also learn whether a
          workspace has any data). */}
      <div className="mb-6">
        <Link
          href={routes.upload(workspaceId)}
          className={buttonClassNames("secondary")}
        >
          <UploadCloud size={16} aria-hidden="true" />
          Upload data
        </Link>
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[2fr_1fr]">
        <div>
          <h2 className="mb-2 text-h3 text-text-primary">Recent reports</h2>
          <RecentReportsPanel />
        </div>
        <div className="hidden lg:block">
          <SemanticLayerSummaryPanel workspaceId={workspaceId} />
        </div>
      </div>
    </main>
  );
}

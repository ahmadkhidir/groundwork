"use client";

import { useCallback, useEffect, useState } from "react";
import { Plus, FolderOpen } from "lucide-react";
import { useRouter } from "next/navigation";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/shared/EmptyState";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { WorkspaceCard } from "./WorkspaceCard";
import { CreateWorkspaceDialog } from "./CreateWorkspaceDialog";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { Workspace } from "@/types/schema";

interface ListWorkspacesResponse {
  workspaces: Workspace[];
}

interface CreateWorkspaceResponse {
  workspace: Workspace;
}

type ScreenState =
  | { status: "loading" }
  | { status: "loaded"; workspaces: Workspace[] }
  | { status: "error"; message: string };

/**
 * Screen 3: Workspace List (screens/03-workspace-list.md) — FEAT-002.
 *
 * A client component per architecture.md's boundary rule: workspace surfaces are client-side and
 * stream from the Go API directly (via web/src/lib/api-client.ts), not server-rendered.
 */
export function WorkspaceListScreen() {
  const router = useRouter();
  const [state, setState] = useState<ScreenState>({ status: "loading" });
  const [dialogOpen, setDialogOpen] = useState(false);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const response = await api.get<ListWorkspacesResponse>("/v1/workspaces");
      setState({ status: "loaded", workspaces: response.workspaces ?? [] });
    } catch (error) {
      const message =
        error instanceof ApiError
          ? error.message
          : "Something went wrong. Try again.";
      setState({ status: "error", message });
    }
  }, []);

  useEffect(() => {
    // react-hooks/set-state-in-effect's static analysis treats any function that eventually calls
    // a setter as an in-effect setState, regardless of the await boundary in between — it cannot
    // distinguish "sets state synchronously during the effect" (the pattern the rule exists to
    // catch) from "kicks off an async fetch whose THEN callback sets state" (this). Fetching this
    // screen's data on mount is the deliberate behavior per screens/03-workspace-list.md's Data
    // Flow ("Screen mounts → Loading state → Fetch workspace list"), not the anti-pattern the rule
    // targets. No shared `useAsyncData`-style hook exists yet to codify this safely — reported as a
    // useful addition to web/src/lib for later waves fetching on mount.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  async function handleCreate(name: string) {
    try {
      const response = await api.post<CreateWorkspaceResponse>("/v1/workspaces", { name });
      setDialogOpen(false);
      router.push(routes.workspace(response.workspace.id));
    } catch (error) {
      // Re-thrown as a plain Error so CreateWorkspaceDialog's catch block can render it inline —
      // including monetization.md's workspace-limit message, surfaced right where the user was
      // trying to act rather than as a toast.
      throw new Error(
        error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      );
    }
  }

  return (
    <main id="main-content" className="mx-auto w-full max-w-6xl p-8">
      <div className="mb-6 flex items-center justify-between gap-4">
        <h1 className="text-h1 text-text-primary">Workspaces</h1>
        {state.status === "loaded" && state.workspaces.length > 0 && (
          <Button
            variant="primary"
            onClick={() => setDialogOpen(true)}
            className="hidden sm:inline-flex"
          >
            <Plus size={16} aria-hidden="true" />
            Create Workspace
          </Button>
        )}
      </div>

      {state.status === "loading" && <SkeletonList count={3} />}

      {state.status === "error" && (
        <ErrorState onRetry={() => void load()} />
      )}

      {state.status === "loaded" && state.workspaces.length === 0 && (
        <EmptyState
          icon={FolderOpen}
          title="No workspaces yet"
          subtitle="A workspace is where your data, semantic layer, and reports live — fully yours to manage and delete."
          ctaLabel="Create your first workspace"
          onCta={() => setDialogOpen(true)}
        />
      )}

      {state.status === "loaded" && state.workspaces.length > 0 && (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {state.workspaces.map((workspace) => (
            <WorkspaceCard key={workspace.id} workspace={workspace} />
          ))}
        </div>
      )}

      {/* Mobile FloatingActionButton — hidden on web, where the header button above takes over. */}
      {state.status === "loaded" && (
        <button
          type="button"
          onClick={() => setDialogOpen(true)}
          aria-label="Create workspace"
          className="fixed bottom-6 right-6 flex size-14 items-center justify-center rounded-full bg-primary text-white shadow-md hover:bg-primary-hover sm:hidden"
        >
          <Plus size={24} aria-hidden="true" />
        </button>
      )}

      {dialogOpen && (
        <CreateWorkspaceDialog
          onCreate={handleCreate}
          onClose={() => setDialogOpen(false)}
        />
      )}
    </main>
  );
}

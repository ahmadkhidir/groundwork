"use client";

import Link from "next/link";
import { useCallback, useEffect, useRef, useState } from "react";
import { ChevronDown, FolderOpen } from "lucide-react";
import { api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { Workspace } from "@/types/schema";

interface ListWorkspacesResponse {
  workspaces: Workspace[];
}

export interface WorkspaceSwitcherProps {
  /** The workspace the current screen belongs to (pathname-derived), if any. */
  currentWorkspaceId?: string;
}

type SwitcherState =
  | { status: "loading" }
  | { status: "loaded"; workspaces: Workspace[] }
  | { status: "error" };

/**
 * The Navigation Shell's workspace switcher (screens.md's Navbar): shows the current workspace's
 * name (or a neutral label when none is selected) and opens a dropdown of every workspace the
 * user can reach, plus the Workspace List. The dropdown mirrors the AccountMenu's interaction
 * contract - outside-click + Escape close, `role="menu"` semantics.
 *
 * Fetch failure is surfaced honestly as an unavailable switcher that still links to the Workspace
 * List (the list screen itself carries the full error state); the shell never fabricates a
 * workspace name.
 */
export function WorkspaceSwitcher({ currentWorkspaceId }: WorkspaceSwitcherProps) {
  const [state, setState] = useState<SwitcherState>({ status: "loading" });
  const [open, setOpen] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const response = await api.get<ListWorkspacesResponse>("/v1/workspaces");
      setState({ status: "loaded", workspaces: response.workspaces ?? [] });
    } catch {
      setState({
        status: "error",
      });
    }
  }, []);

  useEffect(() => {
    // The switcher fetches the workspace list on mount - same mount-triggered async fetch pattern
    // as WorkspaceListScreen (see its eslint note for why this suppression is legitimate).
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

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

  const currentWorkspace =
    state.status === "loaded"
      ? state.workspaces.find((workspace) => workspace.id === currentWorkspaceId)
      : undefined;

  const label = currentWorkspace
    ? currentWorkspace.name
    : state.status === "error"
      ? "Workspaces"
      : "Select workspace";

  return (
    <div ref={containerRef} className="relative">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((value) => !value)}
        disabled={state.status === "loading"}
        className="touch-target inline-flex max-w-52 items-center gap-1.5 rounded-md border border-border bg-surface-secondary px-3 text-body-small font-medium text-text-primary hover:border-primary focus:outline-2 focus:outline-primary disabled:opacity-60 sm:max-w-64"
      >
        {state.status === "loaded" && state.workspaces.length > 0 && (
          <FolderOpen size={15} className="shrink-0 text-text-secondary" aria-hidden="true" />
        )}
        <span className="truncate">{label}</span>
        <ChevronDown
          size={15}
          className={`shrink-0 text-text-secondary transition-transform ${open ? "rotate-180" : ""}`}
          aria-hidden="true"
        />
      </button>

      {open && state.status === "loaded" && (
        <div
          role="menu"
          aria-label="Switch workspace"
          className="absolute left-0 top-full z-50 mt-1 w-64 overflow-hidden rounded-md border border-border bg-surface shadow-md"
        >
          {state.workspaces.map((workspace) => (
            <Link
              key={workspace.id}
              role="menuitem"
              href={routes.workspace(workspace.id)}
              onClick={() => setOpen(false)}
              aria-current={workspace.id === currentWorkspaceId ? "page" : undefined}
              className="flex items-center justify-between gap-2 px-3 py-2 text-body-small text-text-primary hover:bg-surface-secondary"
            >
              <span className="truncate">{workspace.name}</span>
              {workspace.id === currentWorkspaceId && (
                <span className="shrink-0 text-caption font-medium text-text-secondary">
                  Current
                </span>
              )}
            </Link>
          ))}
          <Link
            role="menuitem"
            href={routes.workspaces()}
            onClick={() => setOpen(false)}
            className="block border-t border-border px-3 py-2 text-body-small font-medium text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
          >
            All workspaces
          </Link>
        </div>
      )}

      {open && state.status === "error" && (
        <div
          role="menu"
          aria-label="Switch workspace"
          className="absolute left-0 top-full z-50 mt-1 w-64 overflow-hidden rounded-md border border-border bg-surface shadow-md"
        >
          <Link
            role="menuitem"
            href={routes.workspaces()}
            onClick={() => setOpen(false)}
            className="block px-3 py-2 text-body-small text-text-secondary hover:bg-surface-secondary hover:text-text-primary"
          >
            Workspace list unavailable — open it here
          </Link>
        </div>
      )}

      {open && state.status === "loading" && (
        <div
          role="menu"
          aria-label="Switch workspace"
          className="absolute left-0 top-full z-50 mt-1 w-64 rounded-md border border-border bg-surface p-3 shadow-md"
        >
          <span className="text-body-small text-text-secondary">Loading workspaces…</span>
        </div>
      )}
    </div>
  );
}

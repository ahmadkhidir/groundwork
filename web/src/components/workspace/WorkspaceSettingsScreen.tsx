"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Lock, Trash2 } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button, buttonClassNames } from "@/components/ui/Button";
import { Skeleton } from "@/components/shared/Skeleton";
import { ErrorState } from "@/components/shared/ErrorState";
import { api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { SubscriptionTier, type DeletionRecord, type Workspace } from "@/types/schema";
import { ColumnVisibilityTab } from "./ColumnVisibilityTab";
import { SharingTab } from "./SharingTab";
import { UsageTab } from "./UsageTab";
import { WorkspaceDeleteModal } from "./WorkspaceDeleteModal";
import { storeDeletionProof } from "./DeletedProofScreen";

export interface WorkspaceSettingsScreenProps {
  workspaceId: string;
  /**
   * The user's tier for DISPLAY (which settings surfaces are unlocked). Display-only — the
   * authoritative tier gate is server-side via UserTierLookup, never a client-claimed tier.
   */
  userTier?: SubscriptionTier;
  /**
   * The signed-in user's id, used to decide owner vs read-only member treatment. When absent, the
   * screen assumes ownership (the pre-FEAT-045 behavior, where only owners could reach the route).
   */
  userId?: string;
}

type SettingsTab =
  | "column-visibility"
  | "sharing"
  | "usage"
  | "audit-log"
  | "danger-zone";

const ALL_TABS: { id: SettingsTab; label: string }[] = [
  { id: "column-visibility", label: "Column Visibility" },
  { id: "sharing", label: "Sharing" },
  { id: "usage", label: "Usage" },
  { id: "audit-log", label: "Audit Log" },
  { id: "danger-zone", label: "Danger Zone" },
];

/** The single-workspace GET's payload shape. */
interface GetWorkspaceResponse {
  workspace: Workspace;
}

/** The DELETE /v1/workspaces/:id response: the surviving DeletionRecord proof, per screens/14. */
interface DeleteWorkspaceResponse {
  deletion: DeletionRecord;
}

type AccessState =
  | { status: "loading" }
  | { status: "loaded"; isOwner: boolean; name: string }
  | { status: "error"; message: string };

type DangerZoneState =
  | { status: "loading" }
  | { status: "loaded"; name: string }
  | { status: "error"; message: string };

/**
 * Screen 13: Workspace Settings (screens/13-workspace-settings.md).
 *
 * Five tabs: Column Visibility (FEAT-010, real), Sharing (FEAT-045/FEAT-046, real), Usage
 * (FEAT-047, real), Audit Log (FEAT-042 — link out to the full Egress / Audit Log screen), and
 * Danger Zone (FEAT-043 — the real delete flow with WorkspaceDeleteModal).
 *
 * Read-only shared members (FEAT-045): per screens/13, they see Usage and their own access level
 * only — Column Visibility, Sharing management, Audit Log, and Danger Zone are hidden entirely, not
 * just disabled. The screen compares the workspace's owner_id against the signed-in user's id; the
 * server remains authoritative (a read-only member's mutation requests 403/404 regardless).
 */
export function WorkspaceSettingsScreen({ workspaceId, userTier, userId }: WorkspaceSettingsScreenProps) {
  const router = useRouter();
  const [tab, setTab] = useState<SettingsTab>("audit-log");
  const [access, setAccess] = useState<AccessState>({ status: "loading" });
  const [dangerZone, setDangerZone] = useState<DangerZoneState>({ status: "loading" });
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  const loadWorkspace = useCallback(async () => {
    try {
      const res = await api.get<GetWorkspaceResponse>(`/v1/workspaces/${workspaceId}`);
      const isOwner = userId === undefined || res.workspace.owner_id === userId;
      setAccess({ status: "loaded", isOwner, name: res.workspace.name });
      setDangerZone({ status: "loaded", name: res.workspace.name });
      // A read-only member's only settings surface is Usage (screens/13); land them there.
      if (!isOwner) setTab("usage");
    } catch (error) {
      const message =
        error instanceof Error ? error.message : "Something went wrong. Try again.";
      setAccess({ status: "error", message });
      setDangerZone({ status: "error", message });
    }
  }, [workspaceId, userId]);

  useEffect(() => {
    // Mount-triggered async fetch; see WorkspaceHomeScreen.tsx for why this eslint rule is
    // suppressed here.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void loadWorkspace();
  }, [loadWorkspace]);

  const selectTab = (next: SettingsTab) => {
    setTab(next);
    if (next === "danger-zone" && dangerZone.status === "loading") {
      // The Danger Zone needs the workspace name for the confirm-by-typing gate; if the mount fetch
      // has not resolved yet, fetch it on demand.
      void loadWorkspace();
    }
  };

  const handleDelete = useCallback(async () => {
    if (dangerZone.status !== "loaded") return;
    setDeleting(true);
    setDeleteError(null);
    try {
      const res = await api.delete<DeleteWorkspaceResponse>(`/v1/workspaces/${workspaceId}`);
      // Carry the proof to the Deletion Proof page; survives a refresh of that page in this tab.
      storeDeletionProof(workspaceId, res.deletion);
      router.push(routes.deletionProof(workspaceId));
    } catch (error) {
      // Flow 5 Alternate Path B: no false success. The modal stays open with the server's message
      // and the confirm-by-typing state intact, so the user can retry.
      setDeleting(false);
      setDeleteError(
        error instanceof Error ? error.message : "Something went wrong. Try again.",
      );
    }
  }, [workspaceId, dangerZone, router]);

  const isOwner = access.status === "loaded" ? access.isOwner : true;
  const tabs = isOwner ? ALL_TABS : ALL_TABS.filter((t) => t.id === "usage");
  const tier = userTier ?? SubscriptionTier.Free;

  return (
    <main id="main-content" className="mx-auto w-full max-w-3xl p-8">
      <nav aria-label="Settings navigation" className="mb-4">
        <Link
          href={routes.workspace(workspaceId)}
          className="inline-flex items-center gap-1 text-body-small text-text-secondary hover:text-text-primary"
        >
          <ArrowLeft size={14} aria-hidden="true" />
          Back to workspace
        </Link>
      </nav>

      <h1 className="text-h1 text-text-primary">Workspace Settings</h1>

      <div
        role="tablist"
        aria-label="Workspace settings"
        className="mt-6 flex flex-wrap gap-1 border-b border-border"
      >
        {tabs.map(({ id, label }) => (
          <button
            key={id}
            role="tab"
            id={`settings-tab-${id}`}
            aria-selected={tab === id}
            aria-controls={`settings-panel-${id}`}
            onClick={() => selectTab(id)}
            className={
              tab === id
                ? "border-b-2 border-primary px-3 py-2 text-body font-medium text-text-primary"
                : "border-b-2 border-transparent px-3 py-2 text-body text-text-secondary hover:text-text-primary"
            }
          >
            {label}
          </button>
        ))}
      </div>

      {access.status === "error" && (
        <div className="mt-6">
          <ErrorState description={access.message} onRetry={() => void loadWorkspace()} />
        </div>
      )}

      {access.status === "loaded" && !access.isOwner && (
        <Card className="mt-6">
          <CardHeader
            title="Read-only access"
            description="You can view this workspace's reports and usage, but you cannot change its data, semantic layer, or sharing."
          />
          <p className="flex items-center gap-2 text-body-small text-text-secondary">
            <Lock size={14} aria-hidden="true" />
            {access.name}
          </p>
        </Card>
      )}

      {isOwner && (
        <div
          role="tabpanel"
          id="settings-panel-column-visibility"
          aria-labelledby="settings-tab-column-visibility"
          hidden={tab !== "column-visibility"}
          className="mt-6"
        >
          <ColumnVisibilityTab workspaceId={workspaceId} />
        </div>
      )}

      {isOwner && (
        <div
          role="tabpanel"
          id="settings-panel-sharing"
          aria-labelledby="settings-tab-sharing"
          hidden={tab !== "sharing"}
          className="mt-6"
        >
          <SharingTab workspaceId={workspaceId} tier={tier} />
        </div>
      )}

      <div
        role="tabpanel"
        id="settings-panel-usage"
        aria-labelledby="settings-tab-usage"
        hidden={tab !== "usage"}
        className="mt-6"
      >
        <UsageTab workspaceId={workspaceId} tier={tier} />
      </div>

      {isOwner && (
        <div
          role="tabpanel"
          id="settings-panel-audit-log"
          aria-labelledby="settings-tab-audit-log"
          hidden={tab !== "audit-log"}
          className="mt-6"
        >
          <Card>
            <CardHeader
              title="Audit log"
              description="Every model-egress event from this workspace — what data was sent to the model, when, and for which run."
            />
            <div className="mt-4">
              <Link
                href={routes.auditLog(workspaceId)}
                className={buttonClassNames("outline")}
              >
                View the full audit log
              </Link>
            </div>
          </Card>
        </div>
      )}

      {isOwner && (
        <div
          role="tabpanel"
          id="settings-panel-danger-zone"
          aria-labelledby="settings-tab-danger-zone"
          hidden={tab !== "danger-zone"}
          className="mt-6"
        >
          <Card>
            <CardHeader
              title="Danger zone"
              description="Permanently delete this workspace and every piece of derived state it owns — provably, with confirmation and a proof record (FEAT-043)."
            />
            {dangerZone.status === "loading" ? (
              <Skeleton className="h-10 w-48" />
            ) : dangerZone.status === "error" ? (
              <ErrorState onRetry={() => void loadWorkspace()} />
            ) : (
              <div className="mt-4 flex flex-col items-start justify-between gap-4 sm:flex-row sm:items-center">
                <p className="max-w-md text-body-small text-text-secondary">
                  Deleting is irreversible: object store contents, the semantic layer, reports and
                  conversation history, query caches, and logs are all destroyed in one cascade.
                </p>
                <Button
                  variant="destructive"
                  onClick={() => {
                    setDeleteError(null);
                    setDeleteModalOpen(true);
                  }}
                >
                  <Trash2 size={16} aria-hidden="true" />
                  Delete Workspace
                </Button>
              </div>
            )}
          </Card>

          {dangerZone.status === "loaded" && (
            <WorkspaceDeleteModal
              open={deleteModalOpen}
              workspaceName={dangerZone.name}
              onCancel={() => setDeleteModalOpen(false)}
              onConfirm={() => void handleDelete()}
              loading={deleting}
              error={deleteError ?? undefined}
            />
          )}
        </div>
      )}
    </main>
  );
}

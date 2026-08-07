"use client";

import { useCallback, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { ArrowLeft, Trash2 } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button, buttonClassNames } from "@/components/ui/Button";
import { Skeleton } from "@/components/shared/Skeleton";
import { ErrorState } from "@/components/shared/ErrorState";
import { api } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import type { DeletionRecord, Workspace } from "@/types/schema";
import { WorkspaceDeleteModal } from "./WorkspaceDeleteModal";
import { storeDeletionProof } from "./DeletedProofScreen";

export interface WorkspaceSettingsScreenProps {
  workspaceId: string;
}

type SettingsTab =
  | "column-visibility"
  | "sharing"
  | "usage"
  | "audit-log"
  | "danger-zone";

const TABS: { id: SettingsTab; label: string }[] = [
  { id: "column-visibility", label: "Column Visibility" },
  { id: "sharing", label: "Sharing" },
  { id: "usage", label: "Usage" },
  { id: "audit-log", label: "Audit Log" },
  { id: "danger-zone", label: "Danger Zone" },
];

/** What a later-phase tab will contain, so a Phase-1 user sees an honest boundary rather than a
 * broken-looking empty tab. Styled from the same tokens as the screen; never a fabricated control. */
function LaterPhaseCard({
  feature,
  description,
  phase,
}: {
  feature: string;
  description: string;
  phase: string;
}) {
  return (
    <Card elevation="none">
      <CardHeader title={feature} description={description} />
      <p className="mt-2 text-body-small text-text-secondary">
        Available in a later phase ({phase}).
      </p>
    </Card>
  );
}

/** The single-workspace GET's payload shape; only the name is needed here for the confirm-by-typing gate. */
interface GetWorkspaceResponse {
  workspace: Workspace;
}

/** The DELETE /v1/workspaces/:id response: the surviving DeletionRecord proof, per screens/14. */
interface DeleteWorkspaceResponse {
  deletion: DeletionRecord;
}

type DangerZoneState =
  | { status: "loading" }
  | { status: "loaded"; name: string }
  | { status: "error"; message: string };

/**
 * Screen 13: Workspace Settings (screens/13-workspace-settings.md).
 *
 * The Audit Log tab is functional (FEAT-042 — a short explanatory line plus a button navigating to
 * the full Egress / Audit Log screen, kept as its own route per the screen spec). The Danger Zone
 * tab is the real FEAT-043 delete flow: it loads the workspace name (the confirm-by-typing gate's
 * match value), and "Delete Workspace" opens the screens/14 confirmation modal — explicit scope,
 * type-the-name-to-confirm, honest Executing state, and navigation to the Deletion Proof page on
 * success. The remaining tabs (Column Visibility FEAT-010, Sharing FEAT-045/FEAT-046, Usage
 * FEAT-047) are Phase 5 features and render an explicit later-phase state instead of a fake or
 * half-wired control.
 *
 * Read-only shared members never see the Danger Zone at all — that hiding (FEAT-045, Phase 5) is
 * enforced by the API's owner-only 404 in any case; this screen is only reachable by owners today.
 */
export function WorkspaceSettingsScreen({ workspaceId }: WorkspaceSettingsScreenProps) {
  const router = useRouter();
  const [tab, setTab] = useState<SettingsTab>("audit-log");
  const [dangerZone, setDangerZone] = useState<DangerZoneState>({ status: "loading" });
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState<string | null>(null);

  // Lazy per tab, matching screens/13's Data Flow ("each tab's data loads independently... to avoid
  // fetching sharing/usage data before the user opens those tabs"): the workspace name is only
  // fetched when the Danger Zone is opened, from the click handler rather than an effect.
  const loadWorkspaceName = useCallback(async () => {
    setDangerZone({ status: "loading" });
    try {
      const res = await api.get<GetWorkspaceResponse>(`/v1/workspaces/${workspaceId}`);
      setDangerZone({ status: "loaded", name: res.workspace.name });
    } catch (error) {
      setDangerZone({
        status: "error",
        message:
          error instanceof Error ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  const selectTab = (next: SettingsTab) => {
    setTab(next);
    if (next === "danger-zone" && dangerZone.status === "loading") {
      void loadWorkspaceName();
    }
  };

  const handleDelete = useCallback(async () => {
    if (dangerZone.status !== "loaded") return;
    setDeleting(true);
    setDeleteError(null);
    try {
      const res = await api.delete<DeleteWorkspaceResponse>(
        `/v1/workspaces/${workspaceId}`,
      );
      // Carry the proof to the Deletion Proof page; survives a refresh of that page in this tab.
      storeDeletionProof(workspaceId, res.deletion);
      router.push(routes.deletionProof(workspaceId));
    } catch (error) {
      // Flow 5 Alternate Path B: no false success. The modal stays open with the server's message
      // (which names the failed category) and the confirm-by-typing state intact, so the user can
      // retry — the workspace was not deleted.
      setDeleting(false);
      setDeleteError(
        error instanceof Error ? error.message : "Something went wrong. Try again.",
      );
    }
  }, [workspaceId, dangerZone, router]);

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
        {TABS.map(({ id, label }) => (
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

      <div
        role="tabpanel"
        id="settings-panel-column-visibility"
        aria-labelledby="settings-tab-column-visibility"
        hidden={tab !== "column-visibility"}
        className="mt-6"
      >
        <LaterPhaseCard
          feature="Column visibility"
          description="Lists every ingested column workspace-wide with per-column exclusion toggles and the automatic PII flag (FEAT-010)."
          phase="Phase 5"
        />
      </div>

      <div
        role="tabpanel"
        id="settings-panel-sharing"
        aria-labelledby="settings-tab-sharing"
        hidden={tab !== "sharing"}
        className="mt-6"
      >
        <LaterPhaseCard
          feature="Sharing"
          description="Workspace members (Team tier) and active report share links with revoke actions (FEAT-045, FEAT-046)."
          phase="Phase 5"
        />
      </div>

      <div
        role="tabpanel"
        id="settings-panel-usage"
        aria-labelledby="settings-tab-usage"
        hidden={tab !== "usage"}
        className="mt-6"
      >
        <LaterPhaseCard
          feature="Usage"
          description="Current-period engine-seconds, storage, and model-token usage (FEAT-047)."
          phase="Phase 5"
        />
      </div>

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
            <ErrorState onRetry={() => void loadWorkspaceName()} />
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
    </main>
  );
}

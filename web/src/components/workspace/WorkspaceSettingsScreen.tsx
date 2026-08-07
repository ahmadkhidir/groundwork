"use client";

import { useState } from "react";
import Link from "next/link";
import { ArrowLeft } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { buttonClassNames } from "@/components/ui/Button";
import { routes } from "@/lib/routes";

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

/**
 * Screen 13: Workspace Settings (screens/13-workspace-settings.md).
 *
 * Phase-1 shell, per the authorized scope: the screen is a real tabbed surface, and the Audit Log
 * tab is functional (FEAT-042, Phase 1) — a short explanatory line plus a button that navigates to
 * the full Egress / Audit Log screen (kept as its own route per the screen spec, not a tab body,
 * since the log is a filterable, paginated surface rather than a settings form).
 *
 * The remaining tabs (Column Visibility FEAT-010, Sharing FEAT-045/FEAT-046, Usage FEAT-047,
 * Danger Zone FEAT-043) are Phase 4–5 features outside the authorized scope. Each renders an
 * explicit later-phase state instead of a fake or half-wired control.
 */
export function WorkspaceSettingsScreen({ workspaceId }: WorkspaceSettingsScreenProps) {
  const [tab, setTab] = useState<SettingsTab>("audit-log");

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
            onClick={() => setTab(id)}
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
        <LaterPhaseCard
          feature="Danger zone"
          description="Workspace deletion with confirmation and proof (FEAT-043)."
          phase="Phase 4"
        />
      </div>
    </main>
  );
}
"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { Link2, Lock, UserPlus, Users } from "lucide-react";
import { Card, CardHeader } from "@/components/ui/Card";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { EmptyState } from "@/components/shared/EmptyState";
import { ErrorState } from "@/components/shared/ErrorState";
import { SkeletonList } from "@/components/shared/Skeleton";
import { api, ApiError } from "@/lib/api-client";
import { routes } from "@/lib/routes";
import { SubscriptionTier, type WorkspaceRole } from "@/types/schema";

export interface SharingTabProps {
  workspaceId: string;
  /** The user's tier for DISPLAY (which sharing surfaces are unlocked). Display-only — the
   * authoritative tier gate is server-side via UserTierLookup, never a client-claimed tier. */
  tier: SubscriptionTier;
}

interface WorkspaceMember {
  id: string;
  workspace_id: string;
  user_id: string;
  email: string;
  display_name: string;
  role: WorkspaceRole;
  invited_at: string;
  accepted_at?: string;
}

interface ListMembersResponse {
  members: WorkspaceMember[];
}

interface ReportShare {
  id: string;
  report_id: string;
  share_token: string;
  created_at: string;
}

interface ListSharesResponse {
  shares: ReportShare[];
}

interface ReportSummary {
  id: string;
  title: string;
}

interface ListReportsResponse {
  reports: ReportSummary[];
}

type SectionState =
  | { status: "loading" }
  | { status: "loaded" }
  | { status: "error"; message: string };

/** The shareable URL for a token — the link the owner copies to share the report. */
export function shareLinkUrl(shareToken: string): string {
  return `${window.location.origin}${routes.sharedReport(shareToken)}`;
}

/**
 * FEAT-045/FEAT-046's Sharing tab (screens/13-workspace-settings.md): two sub-sections — "Workspace
 * members" (Team tier; invite by email, role shown as read-only, remove action) and "Shared report
 * links" (Pro/Team; active share links with revoke action).
 *
 * Per monetization.md's Feature Gate Mapping, FEAT-045 is Team-only and FEAT-046 is Pro/Team. On a
 * tier that cannot use a section, the section renders the Upgrade Prompt state rather than fetching
 * (screens/13's "Free-tier users see Sharing... replaced with an Upgrade Prompt state rather than a
 * fetch attempt").
 */
export function SharingTab({ workspaceId, tier }: SharingTabProps) {
  const canShareWorkspace = tier === SubscriptionTier.Team;
  const canShareReports = tier !== SubscriptionTier.Free;

  return (
    <div className="flex flex-col gap-6">
      <WorkspaceMembersSection workspaceId={workspaceId} enabled={canShareWorkspace} />
      <SharedReportLinksSection workspaceId={workspaceId} enabled={canShareReports} />
    </div>
  );
}

function UpgradePromptCard({ feature, plans }: { feature: string; plans: string }) {
  return (
    <Card>
      <CardHeader
        title={feature}
        description={`${plans}. Upgrade to invite read-only members or share report links.`}
      />
      <p className="text-body-small text-text-secondary">
        The authoritative gate is enforced server-side; this section is locked for your current plan.
      </p>
    </Card>
  );
}

function WorkspaceMembersSection({
  workspaceId,
  enabled,
}: {
  workspaceId: string;
  enabled: boolean;
}) {
  const [state, setState] = useState<SectionState>({ status: enabled ? "loading" : "loaded" });
  const [members, setMembers] = useState<WorkspaceMember[]>([]);
  const [email, setEmail] = useState("");
  const [inviting, setInviting] = useState(false);
  const [inviteError, setInviteError] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const res = await api.get<ListMembersResponse>(`/v1/workspaces/${workspaceId}/members`);
      if (!mounted.current) return;
      setMembers(res.members);
      setState({ status: "loaded" });
    } catch (error) {
      if (!mounted.current) return;
      setState({
        status: "error",
        message: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  useEffect(() => {
    if (!enabled) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [enabled, load]);

  const invite = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      const trimmed = email.trim();
      if (!trimmed || inviting) return;
      setInviting(true);
      setInviteError(null);
      try {
        await api.post(`/v1/workspaces/${workspaceId}/members`, { email: trimmed });
        setEmail("");
        await load();
      } catch (error) {
        setInviteError(
          error instanceof ApiError ? error.message : "Something went wrong. Try again.",
        );
      } finally {
        setInviting(false);
      }
    },
    [email, inviting, workspaceId, load],
  );

  const revoke = useCallback(
    async (memberId: string) => {
      if (revokingId) return;
      setRevokingId(memberId);
      setRevokeError(null);
      try {
        await api.delete(`/v1/workspaces/${workspaceId}/members/${memberId}`);
        await load();
      } catch (error) {
        setRevokeError(
          error instanceof ApiError ? error.message : "Something went wrong. Try again.",
        );
      } finally {
        setRevokingId(null);
      }
    },
    [revokingId, workspaceId, load],
  );

  if (!enabled) {
    return (
      <UpgradePromptCard
        feature="Workspace members"
        plans="Workspace sharing is available on the Team plan"
      />
    );
  }

  return (
    <Card>
      <CardHeader
        title="Workspace members"
        description="Invite a colleague with read-only access — they can view reports, charts, and provenance but cannot change your data or semantic layer. Team plan."
      />

      <form className="mb-4 flex items-end gap-2" onSubmit={invite}>
        <div className="flex-1">
          <Input
            label="Invite by email"
            placeholder="colleague@company.com"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            disabled={inviting}
            error={inviteError ?? undefined}
          />
        </div>
        <Button type="submit" variant="primary" disabled={inviting || email.trim().length === 0}>
          <UserPlus size={16} aria-hidden="true" />
          Invite
        </Button>
      </form>

      {revokeError && <p className="mb-3 text-body-small text-error">{revokeError}</p>}

      {state.status === "loading" ? (
        <SkeletonList count={2} />
      ) : state.status === "error" ? (
        <ErrorState onRetry={() => void load()} />
      ) : members.length === 0 ? (
        <EmptyState
          icon={Users}
          title="No members yet"
          subtitle="Invite a colleague above — they'll appear here as read-only members."
        />
      ) : (
        <ul className="flex flex-col gap-2">
          {members.map((member) => (
            <li
              key={member.id}
              className="flex items-center justify-between gap-3 rounded-md border border-border p-3"
            >
              <div className="min-w-0">
                <p className="truncate font-medium text-text-primary">
                  {member.display_name || member.email}
                </p>
                <p className="truncate text-body-small text-text-secondary">{member.email}</p>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <span className="inline-flex items-center gap-1 rounded-sm bg-surface-secondary px-2 py-1 text-caption font-medium uppercase tracking-wide text-text-secondary">
                  <Lock size={12} aria-hidden="true" />
                  read-only
                </span>
                <Button
                  variant="ghost"
                  disabled={revokingId === member.id}
                  loading={revokingId === member.id}
                  onClick={() => void revoke(member.id)}
                >
                  Remove
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

function SharedReportLinksSection({
  workspaceId,
  enabled,
}: {
  workspaceId: string;
  enabled: boolean;
}) {
  const [state, setState] = useState<SectionState>({ status: enabled ? "loading" : "loaded" });
  const [shares, setShares] = useState<ReportShare[]>([]);
  const [reports, setReports] = useState<ReportSummary[]>([]);
  const [selectedReportId, setSelectedReportId] = useState("");
  const [creating, setCreating] = useState(false);
  const [createError, setCreateError] = useState<string | null>(null);
  const [revokingId, setRevokingId] = useState<string | null>(null);
  const [revokeError, setRevokeError] = useState<string | null>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const load = useCallback(async () => {
    setState({ status: "loading" });
    try {
      const [sharesRes, reportsRes] = await Promise.all([
        api.get<ListSharesResponse>(`/v1/workspaces/${workspaceId}/report-shares`),
        api.get<ListReportsResponse>(`/v1/workspaces/${workspaceId}/reports?limit=20`),
      ]);
      if (!mounted.current) return;
      setShares(sharesRes.shares);
      setReports(reportsRes.reports);
      setState({ status: "loaded" });
    } catch (error) {
      if (!mounted.current) return;
      setState({
        status: "error",
        message: error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      });
    }
  }, [workspaceId]);

  useEffect(() => {
    if (!enabled) return;
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [enabled, load]);

  const createShare = useCallback(async () => {
    if (!selectedReportId || creating) return;
    setCreating(true);
    setCreateError(null);
    try {
      await api.post(`/v1/workspaces/${workspaceId}/reports/${selectedReportId}/shares`);
      setSelectedReportId("");
      await load();
    } catch (error) {
      setCreateError(
        error instanceof ApiError ? error.message : "Something went wrong. Try again.",
      );
    } finally {
      setCreating(false);
    }
  }, [selectedReportId, creating, workspaceId, load]);

  const revoke = useCallback(
    async (shareId: string) => {
      if (revokingId) return;
      setRevokingId(shareId);
      setRevokeError(null);
      try {
        await api.delete(`/v1/workspaces/${workspaceId}/report-shares/${shareId}`);
        await load();
      } catch (error) {
        setRevokeError(
          error instanceof ApiError ? error.message : "Something went wrong. Try again.",
        );
      } finally {
        setRevokingId(null);
      }
    },
    [revokingId, workspaceId, load],
  );

  if (!enabled) {
    return (
      <UpgradePromptCard
        feature="Shared report links"
        plans="Shared report links are available on the Pro and Team plans"
      />
    );
  }

  return (
    <Card>
      <CardHeader
        title="Shared report links"
        description="Anyone with a link can view that report read-only, without a workspace invite. Revoking a link makes it stop working on the viewer's next load."
      />

      <div className="mb-4 flex flex-col gap-2 sm:flex-row sm:items-end">
        <div className="flex-1">
          <label htmlFor="share-report-select" className="text-body text-text-primary">
            Share a report
          </label>
          <select
            id="share-report-select"
            value={selectedReportId}
            onChange={(event) => setSelectedReportId(event.target.value)}
            disabled={creating}
            className="h-(--size-input-height) w-full rounded-md border border-border bg-surface px-2 text-body text-text-primary"
          >
            <option value="">Choose a report…</option>
            {reports.map((report) => (
              <option key={report.id} value={report.id}>
                {report.title}
              </option>
            ))}
          </select>
        </div>
        <Button
          variant="primary"
          disabled={!selectedReportId || creating}
          loading={creating}
          onClick={() => void createShare()}
        >
          <Link2 size={16} aria-hidden="true" />
          Create link
        </Button>
      </div>

      {createError && <p className="mb-3 text-body-small text-error">{createError}</p>}
      {revokeError && <p className="mb-3 text-body-small text-error">{revokeError}</p>}

      {state.status === "loading" ? (
        <SkeletonList count={2} />
      ) : state.status === "error" ? (
        <ErrorState onRetry={() => void load()} />
      ) : shares.length === 0 ? (
        <EmptyState
          icon={Link2}
          title="No active share links"
          subtitle="Create a link above to share a report with someone outside this workspace."
        />
      ) : (
        <ul className="flex flex-col gap-2">
          {shares.map((share) => (
            <li
              key={share.id}
              className="flex items-center justify-between gap-3 rounded-md border border-border p-3"
            >
              <div className="min-w-0">
                <p className="truncate font-medium text-text-primary">
                  {shareLinkUrl(share.share_token)}
                </p>
                <p className="text-body-small text-text-secondary">
                  Created {new Date(share.created_at).toLocaleDateString()}
                </p>
              </div>
              <Button
                variant="ghost"
                disabled={revokingId === share.id}
                loading={revokingId === share.id}
                onClick={() => void revoke(share.id)}
                className="shrink-0"
              >
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

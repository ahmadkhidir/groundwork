import { FileText } from "lucide-react";

/**
 * Recent reports section — screens/04-workspace-home.md's "Stacked ListView of recent report Cards"
 * (mobile) / "recent reports Grid/list" (web).
 *
 * FEAT-034 (Recent Reports, Phase 4) owns fetching real ones from
 * `GET /api/workspaces/:id/reports?limit=10`. That endpoint does not exist yet — no run/report
 * feature has shipped in any wave up to and including FEAT-011 — so calling it would 404 rather than
 * degrade gracefully. This renders the screen's own documented "Empty (no reports yet)" state
 * instead, which is also, today, the only HONEST state: there genuinely are no reports for any
 * workspace yet, not a fetch this wave chose to skip.
 */
export function RecentReportsPanel() {
  return (
    <div className="flex flex-col items-center gap-2 rounded-md border border-dashed border-border px-4 py-10 text-center">
      <FileText size={32} className="text-text-secondary" aria-hidden="true" />
      <p className="text-body-small text-text-secondary">
        Your reports will appear here.
      </p>
    </div>
  );
}

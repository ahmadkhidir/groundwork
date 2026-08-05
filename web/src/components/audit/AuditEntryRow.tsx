import { StatusPill } from "@/components/ui/StatusPill";
import { PayloadViewer } from "./PayloadViewer";
import { AUDIT_ACTION_LABELS, type AuditActionTypeFilter, type AuditLogEntryWithScope } from "./types";

export interface AuditEntryProps {
  entry: AuditLogEntryWithScope;
  expanded: boolean;
  onToggle: () => void;
}

function scopeLabelFor(entry: AuditLogEntryWithScope): string {
  return entry.scope === "account" ? "Account" : "This workspace";
}

function actionLabelFor(entry: AuditLogEntryWithScope): string {
  return AUDIT_ACTION_LABELS[entry.action_type as AuditActionTypeFilter] ?? entry.action_type;
}

/**
 * One audit log entry as a mobile Card — screens/17-egress-audit-log.md's mobile list item, tapping
 * expands the full detail in place (see AuditLogScreen's doc comment on why inline expansion stands
 * in for the spec'd Drawer on web too — no Drawer primitive exists yet).
 *
 * A sibling component, AuditEntryTableRow, renders the same entry as a `<tr>` for the web table.
 * They are deliberately SEPARATE components rather than one returning both element shapes in a
 * Fragment: a `<tr>` is not a valid child of a `<ul>` (and a `<li>` is not valid inside a `<tbody>`)
 * — HTML parsers silently relocate misplaced elements, which corrupted both layouts when this was
 * one component toggling visibility with Tailwind's `hidden` utility instead.
 */
export function AuditEntryCard({ entry, expanded, onToggle }: AuditEntryProps) {
  const timestamp = new Date(entry.timestamp);

  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        className="flex w-full flex-col gap-1 rounded-md border border-border bg-surface p-4 text-left"
      >
        <div className="flex items-center justify-between gap-2">
          <StatusPill variant="neutral">{actionLabelFor(entry)}</StatusPill>
          <StatusPill variant={entry.scope === "account" ? "info" : "neutral"}>
            {scopeLabelFor(entry)}
          </StatusPill>
        </div>
        <p className="text-caption text-text-secondary">{timestamp.toLocaleString()}</p>
      </button>
      {expanded && (
        <div className="rounded-b-md border border-t-0 border-border bg-surface-secondary p-4">
          <PayloadViewer payload={entry.payload_sent_to_model ?? ""} />
        </div>
      )}
    </li>
  );
}

/** The web `<table>` counterpart to AuditEntryCard — see its doc comment for why they're separate. */
export function AuditEntryTableRow({ entry, expanded, onToggle }: AuditEntryProps) {
  const timestamp = new Date(entry.timestamp);

  return (
    <>
      <tr
        className="cursor-pointer border-b border-border hover:bg-surface-secondary"
        onClick={onToggle}
        aria-expanded={expanded}
      >
        <td className="px-2 py-2 text-body-small text-text-secondary">
          {timestamp.toLocaleString()}
        </td>
        <td className="px-2 py-2 text-body-small text-text-primary">{entry.user_id}</td>
        <td className="px-2 py-2">
          <StatusPill variant="neutral">{actionLabelFor(entry)}</StatusPill>
        </td>
        <td className="px-2 py-2">
          <StatusPill variant={entry.scope === "account" ? "info" : "neutral"}>
            {scopeLabelFor(entry)}
          </StatusPill>
        </td>
      </tr>
      {expanded && (
        <tr className="border-b border-border bg-surface-secondary">
          <td colSpan={4} className="px-2 py-4">
            <PayloadViewer payload={entry.payload_sent_to_model ?? ""} />
          </td>
        </tr>
      )}
    </>
  );
}

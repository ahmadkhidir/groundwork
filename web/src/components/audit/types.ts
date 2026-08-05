import type { AuditLogEntry } from "@/types/schema";

/**
 * The wire shape GET /v1/workspaces/:id/audit-log actually returns: an AuditLogEntry plus a
 * server-computed `scope` so the client never has to re-derive "This workspace" vs. "Account" from
 * an empty `workspace_id` itself — see server/internal/api/audit.go's toPublicAuditEntry.
 */
export interface AuditLogEntryWithScope extends AuditLogEntry {
  scope: "workspace" | "account";
}

export interface AuditLogListResponse {
  entries: AuditLogEntryWithScope[];
  next_cursor: string;
}

/** Action-type filter values, matching domain.AuditActionType. */
export const AUDIT_ACTION_TYPES = [
  "query_executed",
  "model_call",
  "export",
  "deletion",
  "login",
] as const;

export type AuditActionTypeFilter = (typeof AUDIT_ACTION_TYPES)[number];

/** Human labels for each action type, used by both the filter chips and the entry rows. */
export const AUDIT_ACTION_LABELS: Record<AuditActionTypeFilter, string> = {
  query_executed: "Query",
  model_call: "Model Call",
  export: "Export",
  deletion: "Deletion",
  login: "Login",
};

-- +goose Up
-- +goose StatementBegin

-- FEAT-007's distinguishability requirement, resolved in docs commit c2bc326.
--
-- Before this migration, a column the user explicitly reviewed and cleared ("this is not sensitive")
-- was byte-identical to a column detection never flagged at all: both ended up (pii_flag = FALSE,
-- pii_pattern = ''). schema.md's Column now carries `pii_reviewed_at` for exactly that distinction,
-- and features.md FEAT-007 requires the decision to be "recorded as a timestamped, explicit user
-- decision... so a column that was reviewed-and-cleared is distinguishable later from one that was
-- simply never flagged."
--
-- Nullable, with no default and no backfill: every column that already exists genuinely has never
-- been reviewed, so NULL is the accurate value for all of them. Backfilling any timestamp would
-- fabricate a user decision that never happened — the opposite of what this field exists to prove.
--
-- NULL in the database maps to the empty string in domain.Column, per schema.md's
-- empty-string-means-absent convention and the same NULL <-> "" boundary conversion migration
-- 00002 established for audit_log_entries.workspace_id.
ALTER TABLE columns ADD COLUMN pii_reviewed_at TIMESTAMPTZ;

-- schema.md's AuditLogEntry.action_type gained `pii_override`, a workspace-scoped action type
-- recording that a user explicitly cleared or confirmed a column's pii_flag. It is its own action
-- type rather than a generic note because it governs what the model is allowed to see — a
-- trust-surface decision, not internal bookkeeping — and it is what makes the override visible on
-- screens/17-egress-audit-log.md.
--
-- The CHECK is recreated rather than relaxed: keeping the enum enforced at the database is what has
-- so far prevented an unrecognized action_type from being written by a typo.
ALTER TABLE audit_log_entries DROP CONSTRAINT audit_log_entries_action_type_check;
ALTER TABLE audit_log_entries ADD CONSTRAINT audit_log_entries_action_type_check
    CHECK (action_type IN ('query_executed', 'model_call', 'export', 'deletion', 'login', 'pii_override'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible only while no pii_override entries exist — the same honest-failure posture migration
-- 00002 took for account-scoped login entries. A rollback attempted after PII overrides have been
-- recorded fails at this constraint, which is correct: those rows are trust-surface evidence and a
-- schema rollback is not a licence to silently delete them.
ALTER TABLE audit_log_entries DROP CONSTRAINT audit_log_entries_action_type_check;
ALTER TABLE audit_log_entries ADD CONSTRAINT audit_log_entries_action_type_check
    CHECK (action_type IN ('query_executed', 'model_call', 'export', 'deletion', 'login'));

ALTER TABLE columns DROP COLUMN pii_reviewed_at;

-- +goose StatementEnd

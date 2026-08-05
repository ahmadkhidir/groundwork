-- +goose Up
-- +goose StatementBegin

-- schema.md was corrected after FEAT-001 shipped: AuditLogEntry.workspace_id is no longer required,
-- because `login` is account-scoped — authentication happens before any workspace is selected, and a
-- login is never assigned a placeholder workspace id just to satisfy a NOT NULL column. The original
-- migrations/00001_init.sql made workspace_id `NOT NULL REFERENCES workspaces (id)`, which made
-- login entries unrepresentable; that is why Wave 1 wrote none (see IMPLEMENTATION.md's "Known
-- divergences" table).
--
-- The existing foreign key is left in place deliberately: it still enforces referential integrity
-- for every workspace-scoped row (query_executed, model_call, export, deletion), and a FK does not
-- fire on NULL, so relaxing NOT NULL is the entire change needed. The application layer (see
-- internal/store/postgres/audit.go) converts NULL <-> the empty string at the boundary, so
-- domain.AuditLogEntry keeps schema.md's "empty string means absent" convention on the wire even
-- though the database itself must use NULL to keep the FK meaningful.
ALTER TABLE audit_log_entries ALTER COLUMN workspace_id DROP NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible only if no account-scoped (NULL workspace_id) rows exist yet — true for any environment
-- that hasn't taken this wave's login-audit write live. A downgrade attempted after logins have been
-- recorded will fail at this ALTER, which is the correct, honest outcome: those rows have no
-- workspace to backfill.
ALTER TABLE audit_log_entries ALTER COLUMN workspace_id SET NOT NULL;

-- +goose StatementEnd

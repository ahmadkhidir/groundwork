-- +goose Up
-- +goose StatementBegin

-- Initial schema, transcribed from docs/Groundwork/schema.md.
--
-- TWO RULES RUN THROUGH THIS ENTIRE FILE:
--
-- 1. EVERY table keyed to a workspace carries workspace_id, even where it could be reached by
--    joining through a parent. The deletion cascade's correctness depends on that discipline being
--    universal, not on the cascade query being clever. A table that omits it is a row that survives
--    a deletion the user was told was complete.
--
-- 2. Foreign keys to workspace use ON DELETE CASCADE so the database itself enforces the cascade.
--    Application-level deletion alone would leave orphans behind on any code path that forgot one.
--
-- deletion_records is the deliberate exception to rule 2: it must SURVIVE its workspace, since it is
-- the proof that the workspace was deleted. Its workspace_id is therefore a plain column, not a
-- foreign key.

CREATE TABLE users (
    id            TEXT PRIMARY KEY,
    email         TEXT        NOT NULL UNIQUE,
    display_name  TEXT        NOT NULL,
    -- Password hash is intentionally NOT in schema.md: it is an implementation detail of
    -- authentication, never part of the User entity returned by the API.
    password_hash TEXT        NOT NULL,
    tier          TEXT        NOT NULL DEFAULT 'free' CHECK (tier IN ('free', 'pro', 'team')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE workspaces (
    id            TEXT PRIMARY KEY,
    name          TEXT        NOT NULL,
    owner_id      TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    engine_status TEXT        NOT NULL DEFAULT 'sleeping'
                  CHECK (engine_status IN ('sleeping', 'waking', 'ready')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_workspaces_owner ON workspaces (owner_id);

CREATE TABLE workspace_members (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role         TEXT        NOT NULL CHECK (role IN ('owner', 'read_only')),
    invited_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    accepted_at  TIMESTAMPTZ,
    -- One membership per user per workspace; a second invite updates rather than duplicates.
    UNIQUE (workspace_id, user_id)
);

CREATE INDEX idx_workspace_members_user ON workspace_members (user_id);

CREATE TABLE datasets (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    original_filename TEXT        NOT NULL,
    format            TEXT        NOT NULL CHECK (format IN ('csv', 'excel', 'parquet', 'sqlite')),
    -- URI of the normalized Parquet object in R2 — the canonical stored form. Empty until ingest
    -- completes, matching schema.md's empty-string-means-absent convention.
    parquet_location  TEXT        NOT NULL DEFAULT '',
    ingest_status     TEXT        NOT NULL DEFAULT 'pending'
                      CHECK (ingest_status IN ('pending', 'provisioning', 'profiling', 'complete', 'failed')),
    row_count         BIGINT      NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_datasets_workspace ON datasets (workspace_id);

CREATE TABLE columns (
    id                  TEXT PRIMARY KEY,
    dataset_id          TEXT             NOT NULL REFERENCES datasets (id) ON DELETE CASCADE,
    -- Denormalized from datasets deliberately: see rule 1 at the top of this file.
    workspace_id        TEXT             NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name                TEXT             NOT NULL,
    inferred_type       TEXT             NOT NULL
                        CHECK (inferred_type IN ('string', 'integer', 'number', 'boolean', 'date', 'timestamp')),
    type_confidence     DOUBLE PRECISION NOT NULL CHECK (type_confidence BETWEEN 0 AND 1),
    pii_flag            BOOLEAN          NOT NULL DEFAULT FALSE,
    pii_pattern         TEXT             NOT NULL DEFAULT '',
    -- Keeps the column out of every model payload. The engine may still query it: this governs
    -- egress, not access.
    excluded_from_model BOOLEAN          NOT NULL DEFAULT FALSE,
    null_rate           DOUBLE PRECISION NOT NULL DEFAULT 0 CHECK (null_rate BETWEEN 0 AND 1),
    distinct_count      BIGINT           NOT NULL DEFAULT 0,
    UNIQUE (dataset_id, name)
);

CREATE INDEX idx_columns_dataset ON columns (dataset_id);
CREATE INDEX idx_columns_workspace ON columns (workspace_id);

CREATE TABLE candidate_joins (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT             NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    source_column_id TEXT             NOT NULL REFERENCES columns (id) ON DELETE CASCADE,
    target_column_id TEXT             NOT NULL REFERENCES columns (id) ON DELETE CASCADE,
    confidence_score DOUBLE PRECISION NOT NULL CHECK (confidence_score BETWEEN 0 AND 1),
    -- Plain-language rationale shown to the user. A confidence number alone is not an explanation.
    basis            TEXT             NOT NULL,
    status           TEXT             NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'accepted', 'rejected')),
    created_at       TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX idx_candidate_joins_workspace ON candidate_joins (workspace_id, status);

CREATE TABLE relationships (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    source_column_id TEXT        NOT NULL REFERENCES columns (id) ON DELETE CASCADE,
    target_column_id TEXT        NOT NULL REFERENCES columns (id) ON DELETE CASCADE,
    cardinality      TEXT        NOT NULL
                     CHECK (cardinality IN ('one_to_one', 'one_to_many', 'many_to_one', 'many_to_many')),
    grain_notes      TEXT        NOT NULL DEFAULT '',
    confirmed_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_relationships_workspace ON relationships (workspace_id);

CREATE TABLE metric_definitions (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name           TEXT        NOT NULL,
    sql_expression TEXT        NOT NULL,
    description    TEXT        NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- A metric name must be unambiguous within a workspace, or grounding becomes non-deterministic.
    UNIQUE (workspace_id, name)
);

CREATE TABLE runs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    question_text TEXT        NOT NULL,
    status        TEXT        NOT NULL DEFAULT 'queued'
                  CHECK (status IN ('queued', 'waking', 'running', 'halted', 'failed', 'complete')),
    -- Self-reference for fork-on-amendment. ON DELETE SET NULL rather than CASCADE: deleting a
    -- parent run must not silently destroy the forks derived from it.
    parent_run_id TEXT        REFERENCES runs (id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at  TIMESTAMPTZ
);

CREATE INDEX idx_runs_workspace ON runs (workspace_id, created_at DESC);

CREATE TABLE steps (
    id              TEXT PRIMARY KEY,
    run_id          TEXT NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    workspace_id    TEXT NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    parent_step_id  TEXT REFERENCES steps (id) ON DELETE SET NULL,
    step_name       TEXT NOT NULL CHECK (step_name IN (
                        'profile', 'plan', 'generate_sql', 'static_check', 'execute', 'reconcile',
                        'shadow_check', 'adversarial_review', 'chart_spec', 'narrate', 'assemble')),
    status          TEXT NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'running', 'complete', 'failed', 'halted')),
    -- JSON-encoded, shape varying by step_name. Stored as TEXT rather than JSONB on purpose: these
    -- are opaque payloads replayed verbatim, never queried by their internal structure, and TEXT
    -- guarantees byte-for-byte round-tripping that JSONB's key reordering would break.
    input           TEXT NOT NULL DEFAULT '',
    output          TEXT NOT NULL DEFAULT '',
    -- Makes retry safe. The UNIQUE constraint is the actual enforcement mechanism: a concurrent
    -- double-dispatch fails at the database rather than executing the step twice.
    idempotency_key TEXT NOT NULL UNIQUE,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    -- Lease fields for orchestrator correctness (risk_log R-005). These ship WITH the steps table
    -- from day one; retrofitting leases onto a running orchestrator is what the risk log warns about.
    lease_holder    TEXT        NOT NULL DEFAULT '',
    lease_expires_at TIMESTAMPTZ,
    attempt_count   INTEGER     NOT NULL DEFAULT 0
);

CREATE INDEX idx_steps_run ON steps (run_id);
CREATE INDEX idx_steps_workspace ON steps (workspace_id);
-- Supports the reaper that reclaims steps whose lease expired mid-execution.
CREATE INDEX idx_steps_lease ON steps (lease_expires_at) WHERE lease_expires_at IS NOT NULL;

CREATE TABLE reports (
    id                     TEXT PRIMARY KEY,
    workspace_id           TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id                 TEXT        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    title                  TEXT        NOT NULL,
    narrative              TEXT        NOT NULL,
    -- Inlined value objects, per schema.md: neither has identity outside its report. JSONB here
    -- (unlike steps.input) because these ARE queried structurally, e.g. filtering by chart_type.
    chart_spec             JSONB       NOT NULL,
    reconciliation_summary JSONB       NOT NULL,
    parent_report_id       TEXT        REFERENCES reports (id) ON DELETE SET NULL,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reports_workspace ON reports (workspace_id, created_at DESC);
CREATE INDEX idx_reports_run ON reports (run_id);

CREATE TABLE provenance_links (
    id             TEXT PRIMARY KEY,
    report_id      TEXT   NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    workspace_id   TEXT   NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    claim_text     TEXT   NOT NULL,
    -- Kept as text to preserve exact display formatting (currency symbols, percentages).
    stated_value   TEXT   NOT NULL,
    source_query   TEXT   NOT NULL,
    source_row_ids TEXT[] NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_provenance_links_report ON provenance_links (report_id);

CREATE TABLE report_shares (
    id          TEXT PRIMARY KEY,
    report_id   TEXT        NOT NULL REFERENCES reports (id) ON DELETE CASCADE,
    workspace_id TEXT       NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The only thing standing between a URL and the report's contents, so it must be generated from
    -- a cryptographically secure source.
    share_token TEXT        NOT NULL UNIQUE,
    revoked     BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_report_shares_token ON report_shares (share_token) WHERE NOT revoked;

CREATE TABLE notifications (
    id                  TEXT PRIMARY KEY,
    user_id             TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    type                TEXT        NOT NULL
                        CHECK (type IN ('export_ready', 'engine_slept', 'ingest_failed', 'workspace_invite')),
    message             TEXT        NOT NULL,
    related_entity_type TEXT        NOT NULL CHECK (related_entity_type IN ('report', 'workspace', 'dataset')),
    -- Polymorphic by design (schema.md), resolved by related_entity_type at read time. No foreign
    -- key is possible across three target tables.
    related_entity_id   TEXT        NOT NULL,
    read                BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_notifications_user ON notifications (user_id, read, created_at DESC);

CREATE TABLE audit_log_entries (
    id                    TEXT PRIMARY KEY,
    workspace_id          TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id               TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    action_type           TEXT        NOT NULL
                          CHECK (action_type IN ('query_executed', 'model_call', 'export', 'deletion', 'login')),
    -- Written in the SAME code path as the outbound model call, never reconciled afterwards. An
    -- egress log that can drift from actual egress is not evidence.
    payload_sent_to_model TEXT        NOT NULL DEFAULT '',
    timestamp             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_workspace ON audit_log_entries (workspace_id, timestamp DESC);

CREATE TABLE usage_records (
    id             TEXT PRIMARY KEY,
    workspace_id   TEXT             NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    period_start   TIMESTAMPTZ      NOT NULL,
    period_end     TIMESTAMPTZ      NOT NULL,
    engine_seconds DOUBLE PRECISION NOT NULL DEFAULT 0,
    storage_bytes  BIGINT           NOT NULL DEFAULT 0,
    model_tokens   BIGINT           NOT NULL DEFAULT 0,
    UNIQUE (workspace_id, period_start)
);

CREATE INDEX idx_usage_workspace ON usage_records (workspace_id, period_start DESC);

-- The deliberate exception to the cascade rule.
--
-- This table must SURVIVE the workspace it describes: it is the user's proof that deletion happened.
-- workspace_id is therefore a plain TEXT column with NO foreign key — a reference to something that
-- is intentionally gone. initiated_by likewise does not cascade from users, so deletion history
-- outlives an account closure.
CREATE TABLE deletion_records (
    id                 TEXT PRIMARY KEY,
    workspace_id       TEXT        NOT NULL,
    initiated_by       TEXT        NOT NULL,
    deleted_categories TEXT[]      NOT NULL DEFAULT '{}',
    completed_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_deletion_records_workspace ON deletion_records (workspace_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS deletion_records;
DROP TABLE IF EXISTS usage_records;
DROP TABLE IF EXISTS audit_log_entries;
DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS report_shares;
DROP TABLE IF EXISTS provenance_links;
DROP TABLE IF EXISTS reports;
DROP TABLE IF EXISTS steps;
DROP TABLE IF EXISTS runs;
DROP TABLE IF EXISTS metric_definitions;
DROP TABLE IF EXISTS relationships;
DROP TABLE IF EXISTS candidate_joins;
DROP TABLE IF EXISTS columns;
DROP TABLE IF EXISTS datasets;
DROP TABLE IF EXISTS workspace_members;
DROP TABLE IF EXISTS workspaces;
DROP TABLE IF EXISTS users;

-- +goose StatementEnd

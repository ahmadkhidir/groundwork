-- +goose Up
-- +goose StatementBegin

-- FEAT-019 (Semantic Layer Storage). relationships and metric_definitions already exist as of
-- migration 00001 — that migration transcribed schema.md's full entity list up front, including
-- tables this feature had not yet been authorized to build a read/write surface over (see
-- IMPLEMENTATION.md's "Open items" table). This migration adds the one table schema.md defines
-- that 00001 did not: corrections.
--
-- originating_run_id is a nullable FK with ON DELETE SET NULL, not CASCADE, even though schema.md
-- marks Correction.originating_run_id "required". screens/15-semantic-layer.md's edge cases are
-- explicit that a correction survives its originating run's deletion (e.g. old-run retention
-- pruning): "the correction itself remains valid and listed, but the 'view originating run' link
-- is disabled." Required-at-creation and nullable-after-the-fact are not in tension — every write
-- populates a real run id; only a later, independent deletion of that run can null it out. This
-- mirrors runs.parent_run_id's own SET NULL choice in migration 00001 for the identical reason
-- (a fork must not vanish when its parent is deleted).
CREATE TABLE corrections (
    id                  TEXT        NOT NULL PRIMARY KEY,
    -- Rule 1 (see 00001's header comment): every workspace-keyed table carries workspace_id, even
    -- though it is also reachable by joining through originating_run_id -> runs.workspace_id. The
    -- deletion cascade below depends on this column existing directly on this table.
    workspace_id        TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    description         TEXT        NOT NULL,
    original_logic      TEXT        NOT NULL,
    corrected_logic     TEXT        NOT NULL,
    originating_run_id  TEXT        REFERENCES runs (id) ON DELETE SET NULL,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_corrections_workspace ON corrections (workspace_id, created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS corrections;

-- +goose StatementEnd

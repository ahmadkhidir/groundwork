-- +goose Up
-- FEAT-051's persisted smart-suggestion bucket: model-generated questions accumulate per
-- workspace so a user who picks one question never pays to regenerate the others, and can delete
-- rows they no longer want. Deterministic (standard) suggestions are computed at read time and are
-- deliberately NOT stored here — they stay fresh with the workspace's metadata and cannot be
-- deleted (see api/suggestions.go).
CREATE TABLE suggestion_questions (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    question     TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Exact-text dedupe: the same generated question is never stored twice for a workspace. The
    -- handler also filters against the deterministic set before inserting, so a smart question
    -- identical to a standard one is skipped outright.
    UNIQUE (workspace_id, question)
);

CREATE INDEX idx_suggestion_questions_workspace ON suggestion_questions (workspace_id);

-- +goose Down
DROP TABLE suggestion_questions;

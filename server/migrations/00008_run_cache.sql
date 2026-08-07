-- +goose Up
-- +goose StatementBegin

-- FEAT-027 determinism cache: persists a fully completed run's outputs under a
-- determinism-identical key (workspace + question + schema + seed), so an identical later run
-- replays those outputs instead of re-asking the model and re-running the engine.
--
-- Pass-1 boundary (recorded in the wave report): the key gates on schema SHAPE (columns, types,
-- metrics, relationships via the workspace's complete datasets), not on the underlying DATA rows — a
-- replaced Parquet with identical shape but different contents still matches. A data-version stamp
-- is the recommended Phase-2 hardening. The viewer always surfaces a cache-hit (cache.hit event with
-- cached_run_id), so a replayed answer is never mistaken for freshly computed.
--
-- stored as TEXT (byte-for-byte) rather than JSONB for the same reason the steps table's output is:
-- these are opaque payloads replayed verbatim, never queried by their internal structure.
CREATE TABLE run_cache (
    workspace_id          TEXT        NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    question_hash         TEXT        NOT NULL,
    schema_hash           TEXT        NOT NULL,
    seed_sql              TEXT        NOT NULL DEFAULT '',
    source_run_id         TEXT        NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    plan_output           TEXT        NOT NULL DEFAULT '',
    generate_sql_output   TEXT        NOT NULL,
    execute_output        TEXT        NOT NULL,
    chart_spec_output     TEXT        NOT NULL,
    narrate_output        TEXT        NOT NULL,
    reconciliation_json   JSONB       NOT NULL,
    cached_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, question_hash, schema_hash, seed_sql)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS run_cache;

-- +goose StatementEnd
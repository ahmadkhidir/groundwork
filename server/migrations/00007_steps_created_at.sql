-- +goose Up
-- +goose StatementBegin

-- FEAT-013's run feed (screen 08) orders a run's steps by creation order; the store query
-- (`StepRepository.ListByRun`) does `ORDER BY created_at ASC, id ASC`. But migration 00001's
-- `steps` table shipped WITHOUT a created_at column — only started_at and completed_at — so the
-- first GET on a live run's steps fails with `column "created_at" does not exist` (SQLSTATE 42703)
-- and the Run Viewer 500s instead of rendering the step feed.
--
-- Every other row-keyed table (runs, reports, corrections, workspaces) carries `created_at NOT NULL
-- DEFAULT now()`. `steps` is the sole omission, and its absent column went unnoticed because the
-- store layer has no live-Postgres test exercising ListByRun's ordering clause.
--
-- This adds the column with the same convention, backfilling identical values for existing rows so
-- their relative creation order is preserved. Default now() means ordinary inserts never need to
-- name it, mirroring step Create() which only stamps started_at.
ALTER TABLE steps ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT now();

COMMENT ON COLUMN steps.created_at IS
    'When the step row was created. Drives the run feed''s creation-order sort (ListByRun) and the '
    'deterministic id tiebreak, mirroring created_at on runs/reports/corrections. Existing rows '
    'backfilled to their insert instant via the DEFAULT now().';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Reversible: nothing outside the steps table reads the column, and the feed's ORDER BY is restored
-- to erroring only if a future code path still uses it (which would be a separate, explicit bug).
ALTER TABLE steps DROP COLUMN created_at;

-- +goose StatementEnd
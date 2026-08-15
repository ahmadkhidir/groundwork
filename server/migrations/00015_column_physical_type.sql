-- +goose Up
-- +goose StatementBegin

-- Persist the engine's PHYSICAL column type, for the run path's type-consistency fix.
--
-- columns.inferred_type is the ACTIVE type: what profiling inferred, or the user's FEAT-006
-- override once they set one. A user can override a column to a type its data does not have — a
-- bank statement's "S/N" serial-number column stored as text but overridden to number is the
-- production case. Generated SQL is taught the ACTIVE type and then fails against the real Parquet
-- (DuckDB has no avg(VARCHAR)); smart generation's profiler hit exactly this. The executor must be
-- taught the PHYSICAL type — what SQL must actually execute against — which profiling already reads
-- via DESCRIBE but never persisted separately from the (overridable) inferred type.
--
-- physical_type is set once at ingest to the type the engine reports (never changed by FEAT-006's
-- override, which only rewrites inferred_type). Nullable, no default, no backfill: existing columns
-- predate this migration and are backfilled lazily at view-attach time (run.prepareViews DESCRIBEs
-- the attached views and persists any missing physical type), so the model is taught the real type
-- from the run after the one that attaches the views. NULL maps to the empty string in
-- domain.Column per the empty-string-means-absent convention.

ALTER TABLE columns ADD COLUMN physical_type TEXT;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE columns DROP COLUMN physical_type;

-- +goose StatementEnd

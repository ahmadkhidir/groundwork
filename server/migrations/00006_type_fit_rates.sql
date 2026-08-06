-- +goose Up
-- +goose StatementBegin

-- FEAT-005/FEAT-006: the type-confidence badge on screens/07 must describe whichever type is
-- currently selected for a column, not the one originally inferred.
--
-- Before this, overriding a column's type set type_confidence to a hardcoded 1.0 — so a user who
-- corrected `text` to `whole number` on a column where a third of the values cannot convert was shown
-- HIGH / 100%. A number that is not merely stale but fabricated, on the screen README calls the
-- product's highest-risk UX.
--
-- Storing a fit rate per candidate type is what makes the badge correct without re-querying: the
-- rates are measured once, during profiling, as extra FILTER clauses in the aggregate query that
-- already scans the column. An override then selects among them, which costs nothing and — this is
-- the part that matters operationally — never has to wake a sleeping engine to redraw a badge.
--
-- JSONB rather than six columns: the key set is fixed by the inferred_type enum today, but the
-- product is not done adding types, and this is read as a whole object and never filtered on.
ALTER TABLE columns ADD COLUMN type_fit_rates JSONB NOT NULL DEFAULT '{}'::jsonb;

COMMENT ON COLUMN columns.type_fit_rates IS
    'Fit rate (0-1) per candidate type, measured at profiling. type_confidence is a lookup into this '
    'keyed by the column''s active inferred_type. Empty for rows profiled before migration 00006.';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- No data is lost that cannot be recomputed: type_fit_rates is derived from the Parquet, and
-- type_confidence (the value actually shown) is a separate column that this migration never touched.
ALTER TABLE columns DROP COLUMN type_fit_rates;

-- +goose StatementEnd

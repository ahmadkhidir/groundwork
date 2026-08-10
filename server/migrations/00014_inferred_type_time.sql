-- +goose Up
-- +goose StatementBegin

-- FEAT-005/FEAT-006: add `time` as a seventh column type (schema.md's Column.inferred_type enum).
-- A bare time-of-day column (trading data commonly splits date and time into separate columns,
-- e.g. `09:30:00`) coerces to neither DATE nor TIMESTAMP, so without this it fell back to `string`
-- and could not be honestly reviewed on screens/07's Types step. DuckDB has a native TIME type, so
-- profiling can map and coerce it.
ALTER TABLE columns DROP CONSTRAINT columns_inferred_type_check;
ALTER TABLE columns ADD CONSTRAINT columns_inferred_type_check
    CHECK (inferred_type IN ('string', 'integer', 'number', 'boolean', 'date', 'time', 'timestamp'));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Restore the pre-time enum. Rows whose active type is 'time' (an explicit FEAT-006 override or a
-- profiling result from a TIME-typed normalized column) would then violate the constraint; the
-- migration does not silently rewrite them — an override back to a surviving type is a user
-- decision, and re-running the down migration after such a fix is the expected path.
ALTER TABLE columns DROP CONSTRAINT columns_inferred_type_check;
ALTER TABLE columns ADD CONSTRAINT columns_inferred_type_check
    CHECK (inferred_type IN ('string', 'integer', 'number', 'boolean', 'date', 'timestamp'));

-- +goose StatementEnd

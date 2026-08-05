-- +goose Up
-- +goose StatementBegin

-- FEAT-004 (Wave 3): the Ingest Progress screen (screens/06-ingest-progress.md) requires "a specific
-- error message tied to the failed step" on the Ingest Error state — schema.md's Dataset entity has
-- no field for it (only ingest_status, which is a fixed enum with no room for free text), so this
-- adds one additive column rather than widening ingest_status's CHECK constraint to carry prose.
--
-- NOT NULL DEFAULT '' matches the rest of this table's "empty string means absent" convention
-- (schema.md, mirrored in domain.Dataset's doc comment) rather than using NULL.
ALTER TABLE datasets ADD COLUMN ingest_error TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE datasets DROP COLUMN ingest_error;

-- +goose StatementEnd

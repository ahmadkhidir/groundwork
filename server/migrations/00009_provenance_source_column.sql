-- +goose Up
-- +goose StatementBegin

-- FEAT-030 (Claim-Level Provenance): the Provenance Panel (screens/10) highlights the specific cell
-- that backs a claimed figure. source_row_ids already names the row within the result set; this
-- adds the column name so the panel can pinpoint the cell (e.g. "region" in row 3). Empty string is
-- the safe default for any row written before this migration existed.
ALTER TABLE provenance_links ADD COLUMN source_column TEXT NOT NULL DEFAULT '';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE provenance_links DROP COLUMN source_column;

-- +goose StatementEnd

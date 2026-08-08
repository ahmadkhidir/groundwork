-- +goose Up
-- +goose StatementBegin

-- FEAT-048's distinguishability requirement, per docs commit 171167b (schema.md now specifies
-- Column.type_overridden_at).
--
-- Before this migration, a column the user explicitly overrode (FEAT-006) was byte-identical to a
-- column whose inferred_type was never touched: UpdateInferredType overwrites the same field in
-- place. That made FEAT-048's deletion-impact disclosure ("if the dataset has any columns carrying
-- a type override... states that this review work will be permanently lost") impossible to compute
-- exactly, so the API over-disclosed (has_type_override_risk: true for any dataset with columns).
--
-- Nullable, no default, no backfill — same rationale as pii_reviewed_at (migration 00004): every
-- existing column genuinely has never been overridden, so NULL is the accurate value. NULL maps to
-- the empty string in domain.Column per the empty-string-means-absent convention.

ALTER TABLE columns ADD COLUMN type_overridden_at TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE columns DROP COLUMN type_overridden_at;

-- +goose StatementEnd

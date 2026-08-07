-- +goose Up
-- +goose StatementBegin

-- Billing milestone, pass 2 (Stripe integration). These are SCHEMA additions that schema.md does not
-- model: the documented User entity carries only `tier`, and there is no documented billing-event
-- entity. The Stripe-facing columns below are implementation state the webhook handlers maintain —
-- `users.tier` stays the authoritative tier (UserTierLookup reads it in every gate); these columns
-- exist so the subscription read endpoint and the webhooks can reconcile Stripe objects to accounts.
--
-- Empty-string-means-absent, the schema.md convention (Run.parent_run_id, Column.pii_reviewed_at):
-- a user with no Stripe customer has stripe_customer_id = '' rather than NULL, so scans stay
-- uniform. stripe_subscription_period_end is a nullable timestamp; NULL means "no current period".
ALTER TABLE users ADD COLUMN stripe_customer_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN stripe_subscription_id TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN stripe_subscription_status TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN stripe_subscription_period_end TIMESTAMPTZ;

-- One Stripe customer must map to at most one account. The partial index keeps the near-universal
-- '' default from colliding: with the empty-string-means-absent convention the column is only
-- meaningfully unique among billed users.
CREATE UNIQUE INDEX users_stripe_customer_id_key ON users (stripe_customer_id) WHERE stripe_customer_id <> '';

-- billing_events is the webhook idempotency guard AND the durable billing-event record in one row:
-- event_id is Stripe's own event id and the PRIMARY KEY, so a duplicate delivery of the same event
-- can never insert twice, and the row doubles as the trace record for what the webhook applied.
-- It exists because audit_log_entries' action_type enum has no billing type (schema.md
-- AuditLogEntry) — this table is the documented-in-code alternative, not an enum extension.
CREATE TABLE billing_events (
    event_id   TEXT        PRIMARY KEY,
    event_type TEXT        NOT NULL,
    user_id    TEXT        NOT NULL DEFAULT '',
    data       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_billing_events_created_at ON billing_events (created_at DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS billing_events;
DROP INDEX IF EXISTS users_stripe_customer_id_key;
ALTER TABLE users DROP COLUMN IF EXISTS stripe_subscription_period_end;
ALTER TABLE users DROP COLUMN IF EXISTS stripe_subscription_status;
ALTER TABLE users DROP COLUMN IF EXISTS stripe_subscription_id;
ALTER TABLE users DROP COLUMN IF EXISTS stripe_customer_id;

-- +goose StatementEnd

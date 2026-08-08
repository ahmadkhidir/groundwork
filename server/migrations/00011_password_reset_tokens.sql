-- +goose Up
-- +goose StatementBegin

-- Durable, single-use password reset tokens, per docs/Groundwork/schema.md's PasswordResetToken
-- entity. Tokens are stored as one-way hashes (token_hash): the raw token exists only in the
-- delivered link, never in the database. Rows survive a restart or multiple API instances, so a
-- reset link keeps working across either (architecture.md) — the interim in-memory store did not.

CREATE TABLE password_reset_tokens (
    id          TEXT PRIMARY KEY,
    user_id     TEXT        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash  TEXT        NOT NULL UNIQUE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Consumption invalidates every outstanding token for a user on success (screens/19 step 5), so the
-- user_id lookup is the hot path.
CREATE INDEX idx_password_reset_tokens_user
    ON password_reset_tokens (user_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE password_reset_tokens;

-- +goose StatementEnd

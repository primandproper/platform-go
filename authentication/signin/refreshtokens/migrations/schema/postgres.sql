CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL,
    signed_in_at      TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    purge_after       TIMESTAMPTZ NOT NULL,
    redeemed_at       TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ,
    redeemed_with_key TEXT,
    successor_hash    TEXT
);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_family_idx
    ON signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_subject_idx
    ON signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_purge_after_idx
    ON signin_refresh_tokens (purge_after);


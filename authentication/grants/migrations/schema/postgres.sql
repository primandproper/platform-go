CREATE TABLE IF NOT EXISTS oauth2_grants (
    id                      TEXT PRIMARY KEY,
    scope                   TEXT NOT NULL,
    subject                 TEXT NOT NULL,
    provider                TEXT NOT NULL,
    provider_account_id     TEXT NOT NULL DEFAULT '',
    granted_scopes          TEXT NOT NULL DEFAULT '',
    access_token            BYTEA NOT NULL,
    access_token_expires_at TIMESTAMPTZ,
    refresh_token           BYTEA NOT NULL,
    revocation_reason       TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at         TIMESTAMPTZ,
    archived_at             TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth2_grants_subject_provider_uniq
    ON oauth2_grants (scope, subject, provider);


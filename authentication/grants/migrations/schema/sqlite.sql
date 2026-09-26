CREATE TABLE IF NOT EXISTS oauth2_grants (
    id                      TEXT PRIMARY KEY,
    scope                   TEXT NOT NULL,
    subject                 TEXT NOT NULL,
    provider                TEXT NOT NULL,
    provider_account_id     TEXT NOT NULL DEFAULT '',
    granted_scopes          TEXT NOT NULL DEFAULT '',
    access_token            BLOB NOT NULL,
    access_token_expires_at DATETIME,
    refresh_token           BLOB NOT NULL,
    revocation_reason       TEXT NOT NULL DEFAULT '',
    created_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at         DATETIME,
    archived_at             DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth2_grants_subject_provider_uniq
    ON oauth2_grants (scope, subject, provider);


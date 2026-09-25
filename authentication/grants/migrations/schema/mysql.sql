CREATE TABLE IF NOT EXISTS oauth2_grants (
    id                      VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                   VARCHAR(255) NOT NULL,
    subject                 VARCHAR(255) NOT NULL,
    provider                VARCHAR(64) NOT NULL,
    provider_account_id     VARCHAR(255) NOT NULL DEFAULT '',
    granted_scopes          VARCHAR(2048) NOT NULL DEFAULT '',
    access_token            BLOB NOT NULL,
    access_token_expires_at DATETIME(6),
    refresh_token           BLOB NOT NULL,
    revocation_reason       VARCHAR(32) NOT NULL DEFAULT '',
    created_at              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at         DATETIME(6),
    archived_at             DATETIME(6),
    UNIQUE KEY oauth2_grants_subject_provider_uniq (scope, subject, provider)
);


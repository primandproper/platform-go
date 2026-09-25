-- One row per grant a third party gave this deployment. See the Postgres schema
-- for what the table is for, why scope carries no default, and why the two token
-- columns are ciphertext rather than tokens.
--
-- created_at is CURRENT_TIMESTAMP rather than a value the store binds, and on
-- this dialect that is what makes the column orderable. SQLite has no date type,
-- so a comparison over it is lexicographic text, and CURRENT_TIMESTAMP is what
-- writes the UTC YYYY-MM-DD HH:MM:SS shape that lexicographic order agrees with.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_grants (
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

-- One grant per subject per provider, revoked or not. See the Postgres schema
-- for why this one needs no partial clause.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}oauth2_grants_subject_provider_uniq
    ON {{PREFIX}}oauth2_grants (scope, subject, provider);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    belongs_to_user TEXT NOT NULL,
    credential_id   BLOB NOT NULL,
    public_key      BLOB NOT NULL,
    transports      TEXT NOT NULL DEFAULT '[]',
    friendly_name   TEXT NOT NULL DEFAULT '',
    sign_count      BIGINT NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    last_used_at    DATETIME,
    archived_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS webauthn_credentials_credential_id_uniq
    ON webauthn_credentials (scope, credential_id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS webauthn_credentials_user_idx
    ON webauthn_credentials (scope, belongs_to_user, created_at, id)
    WHERE archived_at IS NULL;


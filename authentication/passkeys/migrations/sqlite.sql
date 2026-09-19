-- One row per registered passkey. See the Postgres schema for what the table is
-- for, why scope carries no default, and why belongs_to_user is not the WebAuthn
-- user handle.
--
-- created_at is CURRENT_TIMESTAMP rather than a value the store binds, and on
-- this dialect that is what makes the column orderable. SQLite has no date type,
-- so a comparison over it is lexicographic text, and CURRENT_TIMESTAMP is what
-- writes the UTC YYYY-MM-DD HH:MM:SS shape that lexicographic order agrees with.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webauthn_credentials (
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

-- One live row per credential, per scope. See the Postgres schema for why the
-- clause is what makes it correct.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}webauthn_credentials_credential_id_uniq
    ON {{PREFIX}}webauthn_credentials (scope, credential_id)
    WHERE archived_at IS NULL;

-- Serves the read every ceremony assembles its webauthn.User from.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webauthn_credentials_user_idx
    ON {{PREFIX}}webauthn_credentials (scope, belongs_to_user, created_at, id)
    WHERE archived_at IS NULL;

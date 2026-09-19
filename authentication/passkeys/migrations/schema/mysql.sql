CREATE TABLE IF NOT EXISTS webauthn_credentials (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    belongs_to_user VARCHAR(64) NOT NULL,
    credential_id   VARBINARY(1023) NOT NULL,
    public_key      BLOB NOT NULL,
    transports      VARCHAR(512) NOT NULL DEFAULT '[]',
    friendly_name   VARCHAR(255) NOT NULL DEFAULT '',
    sign_count      BIGINT NOT NULL DEFAULT 0,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    last_used_at    DATETIME(6),
    archived_at     DATETIME(6),
    live_credential_id VARBINARY(1023)
        GENERATED ALWAYS AS (CASE WHEN archived_at IS NULL THEN credential_id END) VIRTUAL,
    UNIQUE KEY webauthn_credentials_credential_id_uniq (scope, live_credential_id),
    KEY webauthn_credentials_user_idx
        (scope, archived_at, belongs_to_user, created_at, id)
);


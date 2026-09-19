-- One row per registered passkey. See the Postgres schema for what the table is
-- for, why scope carries no default, and why belongs_to_user is not the WebAuthn
-- user handle.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for the
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT.
--
-- credential_id is VARBINARY(1023) because 1023 bytes is the ceiling the
-- WebAuthn specification puts on a credential ID, and this is the one column
-- where a narrower guess would be a passkey the server refuses to store. It
-- shares the unique index with scope, which under utf8mb4 costs four bytes a
-- character: 255 and 1023 together are 2,043 bytes against InnoDB's 3,072-byte
-- index key limit.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webauthn_credentials (
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

    -- MySQL has no partial index, and this is the one index in the module where
    -- that is not a concession to be documented and moved past: the uniqueness
    -- the other two dialects get from `WHERE archived_at IS NULL` is the whole
    -- rule, and a plain UNIQUE KEY here would mean a revoked passkey can never
    -- be registered again on MySQL and can everywhere else.
    --
    -- So the predicate moves into a column. live_credential_id is the credential
    -- id of a live row and NULL of an archived one, and a MySQL unique index
    -- admits any number of NULLs — which is exactly the partial index's
    -- behaviour, spelled where this engine can enforce it. It is generated
    -- rather than written, so no statement can set it and no write can leave it
    -- disagreeing with the two columns it is derived from.
    live_credential_id VARBINARY(1023)
        GENERATED ALWAYS AS (CASE WHEN archived_at IS NULL THEN credential_id END) VIRTUAL,

    UNIQUE KEY {{PREFIX}}webauthn_credentials_credential_id_uniq (scope, live_credential_id),

    -- The reading index covers the whole table, as MySQL's must, and the
    -- predicate column leads: the page filters on archived_at, so putting it in
    -- front keeps the index as selective as the partial clause is elsewhere.
    KEY {{PREFIX}}webauthn_credentials_user_idx
        (scope, archived_at, belongs_to_user, created_at, id)
);

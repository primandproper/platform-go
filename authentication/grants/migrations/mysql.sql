-- One row per grant a third party gave this deployment. See the Postgres schema
-- for what the table is for, why scope carries no default, and why the two token
-- columns are ciphertext rather than tokens.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for the
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The unique key
-- is scope, subject and provider together, which under utf8mb4 costs four bytes
-- a character: 255, 255 and 64 are 2,296 bytes against InnoDB's 3,072-byte
-- index key limit.
--
-- The widths are enforced before a write reaches the server, because strict mode
-- refuses an over-long value here and the other two engines store it whole — see
-- authentication/grants.ErrValueTooLong. The token columns are BLOB, whose
-- 65,535 bytes are well past any token a provider issues plus the frame the
-- encryptor adds.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_grants (
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

    -- One grant per subject per provider, revoked or not. See the Postgres
    -- schema for why this one needs no partial clause on any dialect.
    UNIQUE KEY {{PREFIX}}oauth2_grants_subject_provider_uniq (scope, subject, provider)
);

-- MySQL has no CREATE INDEX IF NOT EXISTS, so the three indexes are declared
-- inline. See postgres.sql for what each one serves, for what the four
-- identifier columns each answer, and for why scope carries no default.
--
-- The TEXT columns are VARCHAR here because every one of them is a key or is
-- indexed, and MySQL cannot index a TEXT column without a prefix length. hash is
-- VARCHAR(255) rather than the 64 characters a hex SHA-256 occupies, so that a
-- deployment configuring a wider hasher does not need a migration to store its
-- output; family_id is VARCHAR(64) to match the rest of this module's minted
-- identifiers; scope, subject_id and active_account_id are VARCHAR(255) because
-- they hold identifiers this table cannot resolve and did not mint.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_refresh_tokens (
    hash              VARCHAR(255) NOT NULL PRIMARY KEY,
    scope             VARCHAR(255) NOT NULL,
    family_id         VARCHAR(64)  NOT NULL,
    subject_id        VARCHAR(255) NOT NULL,
    active_account_id VARCHAR(255) NOT NULL,
    administrative    BOOLEAN      NOT NULL,
    issued_at         DATETIME(6)  NOT NULL,
    expires_at        DATETIME(6)  NOT NULL,
    purge_after       DATETIME(6)  NOT NULL,
    redeemed_at       DATETIME(6),
    revoked_at        DATETIME(6),

    KEY {{PREFIX}}signin_refresh_tokens_family_idx (scope, family_id),
    KEY {{PREFIX}}signin_refresh_tokens_subject_idx (scope, subject_id),
    KEY {{PREFIX}}signin_refresh_tokens_purge_after_idx (purge_after)
);

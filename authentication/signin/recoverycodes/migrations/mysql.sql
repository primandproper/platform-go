-- See postgres.sql for what each column answers, for why the key is the owner
-- and the digest together, for why scope carries no default, and for the
-- columns this table deliberately does not have.
--
-- The TEXT columns are VARCHAR here because every one of them is part of the
-- key, and MySQL cannot index a TEXT column without a prefix length. hash is
-- VARCHAR(255) rather than the 64 characters a hex SHA-256 occupies, so that a
-- deployment configuring a wider hasher does not need a migration to store its
-- output; scope and user_id are VARCHAR(255) because they hold identifiers this
-- table cannot resolve and did not mint.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_recovery_codes (
    scope     VARCHAR(255) NOT NULL,
    user_id   VARCHAR(255) NOT NULL,
    hash      VARCHAR(255) NOT NULL,
    issued_at DATETIME(6)  NOT NULL,
    used_at   DATETIME(6),

    PRIMARY KEY (scope, user_id, hash)
);

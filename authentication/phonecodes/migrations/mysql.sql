-- MySQL has no CREATE INDEX IF NOT EXISTS, so the indexes are declared inline.
-- See postgres.sql for what each column answers, for why scope carries no
-- default, and for what each index serves.
--
-- The TEXT columns are VARCHAR here because MySQL cannot index a TEXT column
-- without a prefix length. scope and subject_id are VARCHAR(255) because they
-- hold identifiers this table did not mint; phone_number is VARCHAR(16), which
-- is E.164's fifteen digits and its plus sign. The unique key is scope and
-- phone_number together, 1,084 bytes under utf8mb4 against InnoDB's 3,072.
-- code_hash is VARCHAR(255) rather than the 64 characters a hex SHA-256 needs,
-- so that a deployment configuring a wider hasher does not need a migration.
--
-- Strict mode refuses an over-long value where the other two engines store it
-- whole, so the widths are enforced before a write reaches the server — see
-- authentication/phonecodes.MaxSubjectLength.
CREATE TABLE IF NOT EXISTS {{PREFIX}}phone_codes (
    id           VARCHAR(64)  NOT NULL PRIMARY KEY,
    scope        VARCHAR(255) NOT NULL,
    subject_id   VARCHAR(255) NOT NULL,
    phone_number VARCHAR(16)  NOT NULL,
    code_hash    VARCHAR(255) NOT NULL,
    attempts     BIGINT       NOT NULL,
    max_attempts BIGINT       NOT NULL,
    issued_at    DATETIME(6)  NOT NULL,
    expires_at   DATETIME(6)  NOT NULL,
    purge_after  DATETIME(6)  NOT NULL,
    redeemed_at  DATETIME(6),
    revoked_at   DATETIME(6),

    UNIQUE KEY {{PREFIX}}phone_codes_phone_uniq (scope, phone_number),
    KEY {{PREFIX}}phone_codes_subject_idx (scope, subject_id),
    KEY {{PREFIX}}phone_codes_purge_after_idx (purge_after)
);

CREATE TABLE IF NOT EXISTS phone_codes (
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
    UNIQUE KEY phone_codes_phone_uniq (scope, phone_number),
    KEY phone_codes_subject_idx (scope, subject_id),
    KEY phone_codes_purge_after_idx (purge_after)
);


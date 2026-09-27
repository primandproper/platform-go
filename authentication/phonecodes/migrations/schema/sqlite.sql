CREATE TABLE IF NOT EXISTS phone_codes (
    id           TEXT PRIMARY KEY,
    scope        TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    phone_number TEXT NOT NULL,
    code_hash    TEXT NOT NULL,
    attempts     INTEGER NOT NULL,
    max_attempts INTEGER NOT NULL,
    issued_at    DATETIME NOT NULL,
    expires_at   DATETIME NOT NULL,
    purge_after  DATETIME NOT NULL,
    redeemed_at  DATETIME,
    revoked_at   DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS phone_codes_phone_uniq
    ON phone_codes (scope, phone_number);

CREATE INDEX IF NOT EXISTS phone_codes_subject_idx
    ON phone_codes (scope, subject_id);

CREATE INDEX IF NOT EXISTS phone_codes_purge_after_idx
    ON phone_codes (purge_after);


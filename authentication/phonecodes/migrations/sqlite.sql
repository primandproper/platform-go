-- See postgres.sql for what each column answers, for why scope carries no
-- default, and for what each index serves.
--
-- SQLite has no date type at all, so these DATETIME columns hold text. Every
-- instant this package binds is a UTC time.Time rendered to whole seconds, which
-- is what keeps the redemption's `expires_at > ?` and the sweep's
-- `purge_after <= ?` chronological comparisons rather than merely lexical ones —
-- see internal/queries.
CREATE TABLE IF NOT EXISTS {{PREFIX}}phone_codes (
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

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_phone_uniq
    ON {{PREFIX}}phone_codes (scope, phone_number);

CREATE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_subject_idx
    ON {{PREFIX}}phone_codes (scope, subject_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_purge_after_idx
    ON {{PREFIX}}phone_codes (purge_after);

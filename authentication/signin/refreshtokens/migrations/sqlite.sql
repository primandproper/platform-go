-- See postgres.sql for what each column answers, for why scope carries no
-- default, for what the three indexes serve, and for what the two idempotency
-- columns are there to tell apart.
--
-- SQLite has no date type at all, so these DATETIME columns hold text. Every
-- instant this package binds is a UTC time.Time and stays one all the way down,
-- which is what keeps the sweep's `purge_after <= ?` a chronological comparison
-- there rather than merely a lexical one — see internal/queries.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    purge_after       DATETIME NOT NULL,
    redeemed_at       DATETIME,
    revoked_at        DATETIME,
    redeemed_with_key TEXT,
    successor_hash    TEXT
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_family_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_subject_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_purge_after_idx
    ON {{PREFIX}}signin_refresh_tokens (purge_after);

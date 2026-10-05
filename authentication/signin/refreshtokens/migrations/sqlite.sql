-- See postgres.sql for what each column answers, for why scope carries no
-- default, and for what the three indexes serve.
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
    -- signed_in_at sits beside issued_at here and after successor_hash in the
    -- other two dialects, because that is where each dialect's v14 run left it:
    -- SQLite cannot add a NOT NULL column without a default, so that run
    -- rebuilt the table to add it, and the rebuild put it here. A table created
    -- here is the table an upgraded one is.
    signed_in_at      DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    purge_after       DATETIME NOT NULL,
    redeemed_at       DATETIME,
    revoked_at        DATETIME,
    redeemed_with_key TEXT,
    successor_hash    TEXT,
    access_token_id   TEXT,
    actor_id          TEXT,
    credential_kind   TEXT
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_family_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_subject_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_purge_after_idx
    ON {{PREFIX}}signin_refresh_tokens (purge_after);

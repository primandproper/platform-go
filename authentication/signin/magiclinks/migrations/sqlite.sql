-- See postgres.sql for what each column answers, for why scope carries no
-- default, for the three columns this table deliberately does not have, and for
-- what the two indexes serve.
--
-- SQLite has no date type at all, so these DATETIME columns hold text. Every
-- instant this package binds is a UTC time.Time and stays one all the way down,
-- which is what keeps the sweep's `purge_after <= ?` a chronological comparison
-- there rather than merely a lexical one — see internal/queries.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_magic_links (
    hash          TEXT PRIMARY KEY,
    scope         TEXT NOT NULL,
    subject_id    TEXT NOT NULL,
    email_address TEXT NOT NULL,
    issued_at     DATETIME NOT NULL,
    expires_at    DATETIME NOT NULL,
    purge_after   DATETIME NOT NULL,
    redeemed_at   DATETIME,
    revoked_at    DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_magic_links_subject_idx
    ON {{PREFIX}}signin_magic_links (scope, subject_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_magic_links_purge_after_idx
    ON {{PREFIX}}signin_magic_links (purge_after);

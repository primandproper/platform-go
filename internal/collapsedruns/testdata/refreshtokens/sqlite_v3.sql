-- Version 3: signed_in_at. See postgres_v3.sql for what the column answers, and
-- for why its backfill is a best-effort reading of the family's earliest
-- surviving issued_at rather than the moment the login began.
--
-- SQLite cannot add a NOT NULL column without a default, and a default is the
-- one thing this column must not have: a mint that forgot it would be stamped
-- with a login time nobody chose. So the table is rebuilt instead. The old one
-- is renamed aside rather than the new one created under a temporary name, so
-- that the only CREATE TABLE here names the table that remains, and nothing
-- reading this schema for its tables finds one that does not exist.
--
-- The order is load-bearing. The rename carries the three indexes with it,
-- names and all, so recreating them has to wait until the DROP has taken them
-- away; created any earlier, each IF NOT EXISTS would find the old table's index
-- under its name and quietly create nothing.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    RENAME TO {{PREFIX}}signin_refresh_tokens_rebuild;

CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         DATETIME NOT NULL,
    signed_in_at      DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    purge_after       DATETIME NOT NULL,
    redeemed_at       DATETIME,
    revoked_at        DATETIME,
    redeemed_with_key TEXT,
    successor_hash    TEXT
);

-- MIN over text is chronological here for the reason the sweep's comparison is:
-- every instant this package binds is a UTC time.Time rendered the same way.
INSERT INTO {{PREFIX}}signin_refresh_tokens (
    hash, scope, family_id, subject_id, active_account_id, administrative,
    issued_at, signed_in_at, expires_at, purge_after,
    redeemed_at, revoked_at, redeemed_with_key, successor_hash
)
SELECT r.hash, r.scope, r.family_id, r.subject_id, r.active_account_id, r.administrative,
       r.issued_at,
       (SELECT MIN(f.issued_at)
          FROM {{PREFIX}}signin_refresh_tokens_rebuild AS f
         WHERE f.scope = r.scope
           AND f.family_id = r.family_id),
       r.expires_at, r.purge_after,
       r.redeemed_at, r.revoked_at, r.redeemed_with_key, r.successor_hash
  FROM {{PREFIX}}signin_refresh_tokens_rebuild AS r;

DROP TABLE {{PREFIX}}signin_refresh_tokens_rebuild;

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_family_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_subject_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_purge_after_idx
    ON {{PREFIX}}signin_refresh_tokens (purge_after);

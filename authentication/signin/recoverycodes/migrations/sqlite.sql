-- See postgres.sql for what each column answers, for why the key is the owner
-- and the digest together, for why scope carries no default, and for the
-- columns this table deliberately does not have.
--
-- SQLite has no date type at all, so these DATETIME columns hold text. Nothing
-- here compares one against a clock — no statement over this table has a
-- deadline — so the whole-second rendering that engine stores is a rounding of
-- a record rather than of a decision.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_recovery_codes (
    scope     TEXT NOT NULL,
    user_id   TEXT NOT NULL,
    hash      TEXT NOT NULL,
    issued_at DATETIME NOT NULL,
    used_at   DATETIME,

    PRIMARY KEY (scope, user_id, hash)
);

-- The two key columns are VARBINARY rather than VARCHAR, for two reasons that
-- point the same way. MySQL's default collation — and MariaDB's more so — folds
-- case, accents and trailing spaces, so under VARCHAR the keys "a", "A" and "a "
-- would be one timer here and three everywhere else; a binary string compares
-- byte for byte, as Postgres and SQLite do. And the bounds are bytes already:
-- MaxKeyLength counts the encoded key's bytes, and a set name of 512 characters
-- is at most 2048 of them. As VARCHAR in utf8mb4 the pair would not fit
-- InnoDB's 3072-byte key limit; as VARBINARY it does, with room for the due
-- index below.
CREATE TABLE IF NOT EXISTS {{PREFIX}}scheduled_timers (
    timer_set       VARBINARY(2048) NOT NULL,
    timer_key       VARBINARY(512)  NOT NULL,
    -- The schedule, as the UTC wall clock at microseconds. It is written from a
    -- count of microseconds since the epoch rather than from a bound time, so
    -- the session's time zone never enters it.
    run_at          DATETIME(6)     NOT NULL,
    -- MEDIUMBLOB rather than BLOB, because BLOB holds 65535 bytes and
    -- MaxPayloadSize is 65536: the largest payload the package admits would
    -- otherwise be the one the column refuses. Nullable for the reason the
    -- Postgres schema gives.
    payload         MEDIUMBLOB      NULL,
    attempts        INT             NOT NULL DEFAULT 0,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6)     NULL,
    archived_at     DATETIME(6)     NULL,
    -- Never-leased and lease-lapsed are the same state to every reader, so
    -- lease_until is NOT NULL and starts at the epoch rather than being
    -- nullable. The due predicate is then one comparison instead of a
    -- comparison plus a NULL branch that every future writer has to remember.
    lease_until     DATETIME(6)     NOT NULL DEFAULT '1970-01-01 00:00:00',
    -- The name of the claim holding that lease. Nullable, where lease_until is
    -- not, for the reason the Postgres schema gives: nothing branches on it, and
    -- a NOT NULL DEFAULT '' would give every unheld row a name.
    leased_by       VARCHAR(64)     NULL,
    fired_at        DATETIME(6)     NULL,
    last_error      TEXT            NULL,

    PRIMARY KEY (timer_set, timer_key),

    -- Serves the claim and the reaper. MySQL has no partial index, so where
    -- Postgres filters fired rows out of the due index this one leads with
    -- fired_at, and the claim's `fired_at IS NULL` is an equality the rest of
    -- the key is ordered under. That is what lets the index hand rows back in
    -- the claim's ORDER BY and stop at the LIMIT, rather than sorting every
    -- candidate — and a locking read that sorted would lock every candidate it
    -- read, not the batch it returned.
    --
    -- The reaper needs no index of its own: (timer_set, fired_at) is this one's
    -- prefix.
    KEY {{PREFIX}}scheduled_timers_due_idx (timer_set, fired_at, run_at, timer_key)
);

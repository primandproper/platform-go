-- No convention triple, and that is deliberate. Completed items are swept so the
-- table stays sized by outstanding work rather than by everything ever queued,
-- which makes archived_at a column that either does nothing or keeps the table
-- growing forever. enqueued_at and available_at are the schedule the claim reads,
-- not a creation stamp and a last mutation.
--
-- The two key columns are VARBINARY rather than VARCHAR, for two reasons that
-- point the same way. MySQL's default collation — and MariaDB's more so — folds
-- case, accents and trailing spaces, so under VARCHAR the keys "a", "A" and "a "
-- would be one row here and three everywhere else; a binary string compares
-- byte for byte, as Postgres and SQLite do. And the bounds are bytes already:
-- MaxKeyLength counts the encoded key's bytes, and a queue name of 512
-- characters is at most 2048 of them. As VARCHAR in utf8mb4 the pair would not
-- fit InnoDB's 3072-byte key limit; as VARBINARY it does, with room for the
-- claim index below.
CREATE TABLE IF NOT EXISTS {{PREFIX}}work_queue_items (
    queue_name   VARBINARY(2048) NOT NULL,
    item_key     VARBINARY(512)  NOT NULL,
    priority     INT             NOT NULL DEFAULT 0,
    attempts     INT             NOT NULL DEFAULT 0,
    enqueued_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    available_at DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    -- Never-leased and lease-lapsed are the same state to every reader, so
    -- lease_until is NOT NULL and starts at the epoch rather than being
    -- nullable. The claim predicate is then one comparison instead of a
    -- comparison plus a NULL branch that every future writer has to remember.
    lease_until  DATETIME(6)     NOT NULL DEFAULT '1970-01-01 00:00:00',
    -- The name of the claim holding that lease. Nullable, where lease_until is
    -- not, for the reason the Postgres schema gives: nothing branches on it, and
    -- a NOT NULL DEFAULT '' would give every unheld row a name.
    leased_by    VARCHAR(64)     NULL,
    completed_at DATETIME(6)     NULL,
    last_error   TEXT            NULL,

    PRIMARY KEY (queue_name, item_key),

    -- Serves the claim. MySQL has no partial index, so where Postgres filters
    -- completed rows out of the index this one leads with completed_at, and the
    -- claim's `completed_at IS NULL` is an equality the rest of the key is
    -- ordered under. That is what lets the index hand rows back in the claim's
    -- ORDER BY and stop at the LIMIT, rather than sorting every candidate — and a
    -- locking read that sorted would lock every candidate it read, not the batch
    -- it returned.
    --
    -- The reaper needs no index of its own: (queue_name, completed_at) is this
    -- one's prefix.
    KEY {{PREFIX}}work_queue_items_claim_idx (queue_name, completed_at, priority DESC, available_at, item_key)
);

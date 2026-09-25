-- No convention triple, and that is deliberate. Completed items are swept so the
-- table stays sized by outstanding work rather than by everything ever queued,
-- which makes archived_at a column that either does nothing or keeps the table
-- growing forever. enqueued_at and available_at are the schedule the claim reads,
-- not a creation stamp and a last mutation.
--
-- The four instants are text in one fixed shape, 'YYYY-MM-DD HH:MM:SS.SSS', and
-- every one of them is written by the server: strftime's %f is the only clock
-- SQLite has that reads finer than a second, and a fixed-width shape is what
-- makes the text comparisons every predicate here makes into time comparisons.
-- CURRENT_TIMESTAMP would be the second-granular spelling of the same clock, and
-- a lease or a delay rounded to a second is a lease that ends early or an item
-- that comes due late; see the workqueue package doc.
CREATE TABLE IF NOT EXISTS {{PREFIX}}work_queue_items (
    queue_name   TEXT     NOT NULL,
    item_key     TEXT     NOT NULL,
    priority     INTEGER  NOT NULL DEFAULT 0,
    attempts     INTEGER  NOT NULL DEFAULT 0,
    enqueued_at  DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    available_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    -- Never-leased and lease-lapsed are the same state to every reader, so
    -- lease_until is NOT NULL and starts at the epoch rather than being
    -- nullable. The claim predicate is then one comparison instead of a
    -- comparison plus a NULL branch that every future writer has to remember.
    lease_until  DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00.000',
    -- The name of the claim holding that lease. Nullable, where lease_until is
    -- not, for the reason the Postgres schema gives: nothing branches on it, and
    -- a NOT NULL DEFAULT '' would give every unheld row a name.
    leased_by    TEXT,
    completed_at DATETIME,
    last_error   TEXT,

    PRIMARY KEY (queue_name, item_key)
);

-- Serves the claim, and sized by backlog rather than by history for the same
-- reason as the Postgres index: completed rows are excluded.
CREATE INDEX IF NOT EXISTS {{PREFIX}}work_queue_items_claim_idx
    ON {{PREFIX}}work_queue_items (queue_name, priority DESC, available_at, item_key)
    WHERE completed_at IS NULL;

-- Serves the reaper, and nothing else looks at completed rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}work_queue_items_reap_idx
    ON {{PREFIX}}work_queue_items (queue_name, completed_at)
    WHERE completed_at IS NOT NULL;

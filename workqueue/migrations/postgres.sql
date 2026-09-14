-- No convention triple, and that is deliberate. Completed items are swept so the
-- table stays sized by outstanding work rather than by everything ever queued,
-- which makes archived_at a column that either does nothing or keeps the table
-- growing forever. enqueued_at and available_at are the schedule the claim reads,
-- not a creation stamp and a last mutation.
CREATE TABLE IF NOT EXISTS {{PREFIX}}work_queue_items (
    queue_name   TEXT        NOT NULL,
    item_key     TEXT        NOT NULL,
    priority     INTEGER     NOT NULL DEFAULT 0,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- Never-leased and lease-lapsed are the same state to every reader, so
    -- lease_until is NOT NULL and starts at the epoch rather than being
    -- nullable. The claim predicate is then one comparison instead of a
    -- comparison plus a NULL branch that every future writer has to remember.
    lease_until  TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    -- The name of the claim holding that lease, which the completion and the
    -- hand-back present again so a worker whose lease lapsed cannot retire or
    -- release an item somebody else has since taken.
    --
    -- Nullable, where lease_until is not, and the two are not inconsistent. The
    -- epoch sentinel above exists because the claim predicate branches on the
    -- horizon; nothing branches on the holder, whose only reader is a
    -- membership test that already treats NULL as no match. NOT NULL DEFAULT ''
    -- would instead give every unheld row a name — the empty string, which is
    -- what a caller who forgot to pass one would bind.
    leased_by    TEXT,
    completed_at TIMESTAMPTZ,
    last_error   TEXT,

    -- One table serves every logical queue in the database, so the queue name
    -- leads the key. It is also the lock order every writer of this table
    -- acquires rows in.
    PRIMARY KEY (queue_name, item_key)
);

-- Serves the claim: the predicate filters on queue and completion, and the
-- ORDER BY is (priority DESC, available_at, item_key). The partial clause is
-- what keeps this index sized by backlog rather than by total history —
-- completed rows outnumber pending ones by orders of magnitude between reaps,
-- and without it a claim slows down as the table fills.
CREATE INDEX IF NOT EXISTS {{PREFIX}}work_queue_items_claim_idx
    ON {{PREFIX}}work_queue_items (queue_name, priority DESC, available_at, item_key)
    WHERE completed_at IS NULL;

-- Serves the reaper, and nothing else looks at completed rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}work_queue_items_reap_idx
    ON {{PREFIX}}work_queue_items (queue_name, completed_at)
    WHERE completed_at IS NOT NULL;

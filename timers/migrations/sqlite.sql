-- The instants are text in one fixed shape, 'YYYY-MM-DD HH:MM:SS.SSS', and the
-- server writes every one of them but run_at: strftime's %f is the only clock
-- SQLite has that reads finer than a second, and a fixed-width shape is what
-- makes the text comparisons every predicate here makes into time comparisons.
-- CURRENT_TIMESTAMP would be the second-granular spelling of the same clock,
-- and a lease rounded to a second is a lease that ends early.
--
-- run_at is the caller's, and arrives as a count of microseconds that the
-- statement rounds up to the millisecond before storing it in the same shape.
-- Up, because a timer stored a fraction of a millisecond late fires late and
-- one stored early fires early, and only one of those is a scheduler.
CREATE TABLE IF NOT EXISTS {{PREFIX}}scheduled_timers (
    timer_set       TEXT     NOT NULL,
    timer_key       TEXT     NOT NULL,
    run_at          DATETIME NOT NULL,
    -- Nullable rather than defaulting to an empty blob: "no payload" and "an
    -- empty payload" are different statements about the timer.
    payload         BLOB,
    attempts        INTEGER  NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    last_updated_at DATETIME,
    archived_at     DATETIME,
    -- Never-leased and lease-lapsed are the same state to every reader, so
    -- lease_until is NOT NULL and starts at the epoch rather than being
    -- nullable. The due predicate is then one comparison instead of a
    -- comparison plus a NULL branch that every future writer has to remember.
    lease_until     DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00.000',
    -- The name of the claim holding that lease. Nullable, where lease_until is
    -- not, for the reason the Postgres schema gives: nothing branches on it, and
    -- a NOT NULL DEFAULT '' would give every unheld row a name.
    leased_by       TEXT,
    fired_at        DATETIME,
    last_error      TEXT,

    PRIMARY KEY (timer_set, timer_key)
);

-- Serves the claim, and sized by outstanding timers rather than by history for
-- the same reason as the Postgres index: fired rows are excluded.
CREATE INDEX IF NOT EXISTS {{PREFIX}}scheduled_timers_due_idx
    ON {{PREFIX}}scheduled_timers (timer_set, run_at, timer_key)
    WHERE fired_at IS NULL;

-- Serves the reaper, and nothing else looks at fired rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}scheduled_timers_reap_idx
    ON {{PREFIX}}scheduled_timers (timer_set, fired_at)
    WHERE fired_at IS NOT NULL;

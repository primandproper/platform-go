-- One row per standing rule: "Tuesdays at 4pm, every week, from September 2".
--
-- The rule means nothing here. What an occurrence of it is for — a lesson, a
-- grooming, a shift — is the consumer's, kept in the consumer's own table keyed
-- by occurrence id, the same bargain comments makes with an opaque target.
--
-- scope is whose rule this is, and like every scope column in the module it has
-- no default: the empty string is tenancy.Global(), and a column that supplied
-- it for a write which did not name one would hand the global scope to whoever
-- forgot the column.
--
-- time_zone is an IANA name, and the rule's wall-clock facts are read in it:
-- weekday (0 is Sunday, as Go's time.Weekday counts), start_minute (minutes past
-- local midnight), interval_weeks (1 is every week). starts_on and ends_on are
-- calendar dates, YYYY-MM-DD, as text on every dialect — a date in a time zone
-- is not an instant, and a DATE column would invite a driver to make it one.
-- ends_on is the first date with no occurrence, and the empty string is a rule
-- with no end.
--
-- materialized_until is how far the rule has been written out: every
-- occurrence it implies before this instant has a row in series_occurrences.
-- exhausted_at is when a pass found nothing left to write — the rule ended
-- before the horizon reached it — and is what takes a series off the horizon
-- worker's list for good.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series (
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    time_zone          TEXT NOT NULL,
    weekday            BIGINT NOT NULL,
    start_minute       BIGINT NOT NULL,
    interval_weeks     BIGINT NOT NULL,
    starts_on          TEXT NOT NULL,
    ends_on            TEXT NOT NULL DEFAULT '',
    materialized_until TIMESTAMPTZ NOT NULL,
    exhausted_at       TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at    TIMESTAMPTZ,
    archived_at        TIMESTAMPTZ
);

-- A scope's series, in id order: the listing, and the walk a closure makes
-- before it skips.
CREATE INDEX IF NOT EXISTS {{PREFIX}}series_scope_idx
    ON {{PREFIX}}series (scope, id);

-- The horizon worker's read: live, unexhausted series, least far written first.
CREATE INDEX IF NOT EXISTS {{PREFIX}}series_due_idx
    ON {{PREFIX}}series (materialized_until, id);

-- One row per instance a series implies, plus one per replacement a consumer
-- added against a skipped one.
--
-- state is only about existence: 'scheduled', 'skipped' or 'moved'. What
-- happened when an occurrence did happen — attended, no-show, cancelled late —
-- is not here; it is the consumer's, keyed by id.
--
-- slot_at is the instant the rule put the occurrence at, and never changes:
-- moving an occurrence changes scheduled_at and leaves slot_at where the rule
-- said, which is what keeps the horizon worker from writing the slot a second
-- time. A replacement has no slot — no rule implies it — and its slot_at is
-- NULL, which the unique index below does not count as a collision.
--
-- replaced_by names the occurrence added in a skipped one's place, and the empty
-- string is one with no replacement. reason is free text the consumer wrote
-- with the skip, the move or the closure.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series_occurrences (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    series_id       TEXT NOT NULL,
    scheduled_at    TIMESTAMPTZ NOT NULL,
    slot_at         TIMESTAMPTZ,
    state           TEXT NOT NULL,
    replaced_by     TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

-- One row per slot per series. It is what makes materializing idempotent: the
-- worker inserts every slot it computes and the index turns a slot already
-- written into a write that does nothing, so two passes over one series — two
-- replicas, or a closure and the worker — leave one row per slot. It is also the
-- insert's conflict target.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}series_occurrences_slot_uniq
    ON {{PREFIX}}series_occurrences (series_id, slot_at);

-- The week view: a scope's occurrences by when they happen.
CREATE INDEX IF NOT EXISTS {{PREFIX}}series_occurrences_window_idx
    ON {{PREFIX}}series_occurrences (scope, scheduled_at);

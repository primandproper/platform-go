-- One row per standing rule. See the Postgres schema for what each column means,
-- why scope carries no default, and why the two dates are text.
--
-- created_at is CURRENT_TIMESTAMP rather than a value the store binds, and on
-- this dialect that is what makes the column orderable. SQLite has no date type,
-- so a comparison over it is lexicographic text, and CURRENT_TIMESTAMP writes
-- the UTC YYYY-MM-DD HH:MM:SS shape the store binds every other time in.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series (
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    time_zone          TEXT NOT NULL,
    weekday            BIGINT NOT NULL,
    start_minute       BIGINT NOT NULL,
    interval_weeks     BIGINT NOT NULL,
    starts_on          TEXT NOT NULL,
    ends_on            TEXT NOT NULL DEFAULT '',
    materialized_until DATETIME NOT NULL,
    exhausted_at       DATETIME,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at    DATETIME,
    archived_at        DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}series_scope_idx
    ON {{PREFIX}}series (scope, id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}series_due_idx
    ON {{PREFIX}}series (materialized_until, id);

-- One row per instance a series implies, plus one per replacement. See the
-- Postgres schema for what state, slot_at and replaced_by mean.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series_occurrences (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    series_id       TEXT NOT NULL,
    scheduled_at    DATETIME NOT NULL,
    slot_at         DATETIME,
    state           TEXT NOT NULL,
    replaced_by     TEXT NOT NULL DEFAULT '',
    reason          TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}series_occurrences_slot_uniq
    ON {{PREFIX}}series_occurrences (series_id, slot_at);

CREATE INDEX IF NOT EXISTS {{PREFIX}}series_occurrences_window_idx
    ON {{PREFIX}}series_occurrences (scope, scheduled_at);

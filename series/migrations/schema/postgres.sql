CREATE TABLE IF NOT EXISTS series (
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

CREATE INDEX IF NOT EXISTS series_scope_idx
    ON series (scope, id);

CREATE INDEX IF NOT EXISTS series_due_idx
    ON series (materialized_until, id);

CREATE TABLE IF NOT EXISTS series_occurrences (
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

CREATE UNIQUE INDEX IF NOT EXISTS series_occurrences_slot_uniq
    ON series_occurrences (series_id, slot_at);

CREATE INDEX IF NOT EXISTS series_occurrences_window_idx
    ON series_occurrences (scope, scheduled_at);


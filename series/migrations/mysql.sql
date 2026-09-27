-- One row per standing rule. See the Postgres schema for what each column means,
-- why scope carries no default, and why the two dates are text.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for the
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- enforced before a write reaches the server — see series.ErrValueTooLong —
-- because strict mode refuses an over-long value here, the other two engines
-- store it whole, and the materializing insert's IGNORE would truncate it and
-- report success.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series (
    id                 VARCHAR(64) NOT NULL PRIMARY KEY,
    scope              VARCHAR(255) NOT NULL,
    time_zone          VARCHAR(64) NOT NULL,
    weekday            BIGINT NOT NULL,
    start_minute       BIGINT NOT NULL,
    interval_weeks     BIGINT NOT NULL,
    starts_on          VARCHAR(10) NOT NULL,
    ends_on            VARCHAR(10) NOT NULL DEFAULT '',
    materialized_until DATETIME(6) NOT NULL,
    exhausted_at       DATETIME(6),
    created_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at    DATETIME(6),
    archived_at        DATETIME(6),

    KEY {{PREFIX}}series_scope_idx (scope, id),
    KEY {{PREFIX}}series_due_idx (materialized_until, id)
);

-- One row per instance a series implies, plus one per replacement. See the
-- Postgres schema for what state, slot_at and replaced_by mean.
CREATE TABLE IF NOT EXISTS {{PREFIX}}series_occurrences (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    series_id       VARCHAR(64) NOT NULL,
    scheduled_at    DATETIME(6) NOT NULL,
    slot_at         DATETIME(6),
    state           VARCHAR(16) NOT NULL,
    replaced_by     VARCHAR(64) NOT NULL DEFAULT '',
    reason          VARCHAR(1024) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- One row per slot per series, and the materializing insert's conflict
    -- target. A NULL slot_at — a replacement — collides with nothing, as it
    -- does on the other two dialects.
    UNIQUE KEY {{PREFIX}}series_occurrences_slot_uniq (series_id, slot_at),
    KEY {{PREFIX}}series_occurrences_window_idx (scope, scheduled_at)
);

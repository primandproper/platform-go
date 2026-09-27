CREATE TABLE IF NOT EXISTS series (
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
    KEY series_scope_idx (scope, id),
    KEY series_due_idx (materialized_until, id)
);

CREATE TABLE IF NOT EXISTS series_occurrences (
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
    UNIQUE KEY series_occurrences_slot_uniq (series_id, slot_at),
    KEY series_occurrences_window_idx (scope, scheduled_at)
);


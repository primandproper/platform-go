CREATE TABLE IF NOT EXISTS metering_events (
    scope           TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    subject         TEXT NOT NULL,
    meter           TEXT NOT NULL,
    quantity        INTEGER NOT NULL,
    occurred_at     DATETIME NOT NULL,
    recorded_at     DATETIME NOT NULL,
    period_start    DATETIME NOT NULL,
    dimensions      BLOB,
    PRIMARY KEY (scope, meter, idempotency_key)
);

CREATE INDEX IF NOT EXISTS metering_events_period_idx
    ON metering_events (scope, subject, meter, period_start, occurred_at);

CREATE INDEX IF NOT EXISTS metering_events_reap_idx
    ON metering_events (recorded_at);

CREATE TABLE IF NOT EXISTS metering_totals (
    scope            TEXT NOT NULL,
    subject          TEXT NOT NULL,
    meter            TEXT NOT NULL,
    period_start     DATETIME NOT NULL,
    period_end       DATETIME NOT NULL,
    aggregation      TEXT NOT NULL,
    quantity         INTEGER NOT NULL DEFAULT 0,
    last_occurred_at DATETIME NOT NULL,
    claimed_quantity INTEGER NOT NULL DEFAULT 0,
    flushed_quantity INTEGER NOT NULL DEFAULT 0,
    flush_sequence   INTEGER NOT NULL DEFAULT 0,
    flush_attempts   INTEGER NOT NULL DEFAULT 0,
    next_flush       DATETIME NOT NULL,
    claimed_until    DATETIME,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at  DATETIME,
    archived_at      DATETIME,
    PRIMARY KEY (scope, subject, meter, period_start)
);

CREATE INDEX IF NOT EXISTS metering_totals_flush_idx
    ON metering_totals (next_flush, scope, subject, meter)
    WHERE quantity > flushed_quantity;

CREATE INDEX IF NOT EXISTS metering_totals_subject_idx
    ON metering_totals (scope, subject, period_start, meter);


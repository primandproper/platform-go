CREATE TABLE IF NOT EXISTS metering_events (
    scope           TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    subject         TEXT NOT NULL,
    meter           TEXT NOT NULL,
    quantity        BIGINT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL,
    recorded_at     TIMESTAMPTZ NOT NULL,
    period_start    TIMESTAMPTZ NOT NULL,
    dimensions      BYTEA,
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
    period_start     TIMESTAMPTZ NOT NULL,
    period_end       TIMESTAMPTZ NOT NULL,
    aggregation      TEXT NOT NULL,
    quantity         BIGINT NOT NULL DEFAULT 0,
    last_occurred_at TIMESTAMPTZ NOT NULL,
    flushed_quantity BIGINT NOT NULL DEFAULT 0,
    flush_sequence   INTEGER NOT NULL DEFAULT 0,
    flush_attempts   INTEGER NOT NULL DEFAULT 0,
    next_flush       TIMESTAMPTZ NOT NULL,
    claimed_until    TIMESTAMPTZ,
    last_error       TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at  TIMESTAMPTZ,
    archived_at      TIMESTAMPTZ,
    PRIMARY KEY (scope, subject, meter, period_start)
);

CREATE INDEX IF NOT EXISTS metering_totals_flush_idx
    ON metering_totals (next_flush, scope, subject, meter)
    WHERE quantity > flushed_quantity;

CREATE INDEX IF NOT EXISTS metering_totals_subject_idx
    ON metering_totals (scope, subject, period_start DESC, meter);


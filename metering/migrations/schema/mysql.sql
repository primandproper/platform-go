CREATE TABLE IF NOT EXISTS metering_events (
    scope           VARCHAR(255) NOT NULL,
    idempotency_key VARCHAR(255) NOT NULL,
    subject         VARCHAR(255) NOT NULL,
    meter           VARCHAR(64) NOT NULL,
    quantity        BIGINT NOT NULL,
    occurred_at     DATETIME(6) NOT NULL,
    recorded_at     DATETIME(6) NOT NULL,
    period_start    DATETIME(6) NOT NULL,
    dimensions      BLOB,
    PRIMARY KEY (scope, meter, idempotency_key),
    KEY metering_events_period_idx
        (scope, subject, meter, period_start, occurred_at),
    KEY metering_events_reap_idx (recorded_at)
);

CREATE TABLE IF NOT EXISTS metering_totals (
    scope            VARCHAR(255) NOT NULL,
    subject          VARCHAR(255) NOT NULL,
    meter            VARCHAR(64) NOT NULL,
    period_start     DATETIME(6) NOT NULL,
    period_end       DATETIME(6) NOT NULL,
    aggregation      VARCHAR(32) NOT NULL,
    quantity         BIGINT NOT NULL DEFAULT 0,
    last_occurred_at DATETIME(6) NOT NULL,
    claimed_quantity BIGINT NOT NULL DEFAULT 0,
    flushed_quantity BIGINT NOT NULL DEFAULT 0,
    flush_sequence   INT NOT NULL DEFAULT 0,
    flush_attempts   INT NOT NULL DEFAULT 0,
    next_flush       DATETIME(6) NOT NULL,
    claimed_until    DATETIME(6),
    last_error       TEXT NOT NULL,
    created_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at  DATETIME(6),
    archived_at      DATETIME(6),
    PRIMARY KEY (scope, subject, meter, period_start),
    KEY metering_totals_flush_idx (next_flush, scope, subject, meter),
    KEY metering_totals_subject_idx (scope, subject, period_start, meter)
);


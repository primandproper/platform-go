CREATE TABLE IF NOT EXISTS scheduled_timers (
    timer_set       TEXT        NOT NULL,
    timer_key       TEXT        NOT NULL,
    run_at          TIMESTAMPTZ NOT NULL,
    payload         BYTEA,
    attempts        INTEGER     NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    lease_until     TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    leased_by       TEXT,
    fired_at        TIMESTAMPTZ,
    last_error      TEXT,
    PRIMARY KEY (timer_set, timer_key)
);

CREATE INDEX IF NOT EXISTS scheduled_timers_due_idx
    ON scheduled_timers (timer_set, run_at, timer_key)
    WHERE fired_at IS NULL;

CREATE INDEX IF NOT EXISTS scheduled_timers_reap_idx
    ON scheduled_timers (timer_set, fired_at)
    WHERE fired_at IS NOT NULL;


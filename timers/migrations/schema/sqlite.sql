CREATE TABLE IF NOT EXISTS scheduled_timers (
    timer_set       TEXT     NOT NULL,
    timer_key       TEXT     NOT NULL,
    run_at          DATETIME NOT NULL,
    payload         BLOB,
    attempts        INTEGER  NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    last_updated_at DATETIME,
    archived_at     DATETIME,
    lease_until     DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00.000',
    leased_by       TEXT,
    fired_at        DATETIME,
    last_error      TEXT,
    PRIMARY KEY (timer_set, timer_key)
);

CREATE INDEX IF NOT EXISTS scheduled_timers_due_idx
    ON scheduled_timers (timer_set, run_at, timer_key)
    WHERE fired_at IS NULL;

CREATE INDEX IF NOT EXISTS scheduled_timers_reap_idx
    ON scheduled_timers (timer_set, fired_at)
    WHERE fired_at IS NOT NULL;


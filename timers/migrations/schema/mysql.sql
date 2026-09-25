CREATE TABLE IF NOT EXISTS scheduled_timers (
    timer_set       VARBINARY(2048) NOT NULL,
    timer_key       VARBINARY(512)  NOT NULL,
    run_at          DATETIME(6)     NOT NULL,
    payload         MEDIUMBLOB      NULL,
    attempts        INT             NOT NULL DEFAULT 0,
    created_at      DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6)     NULL,
    archived_at     DATETIME(6)     NULL,
    lease_until     DATETIME(6)     NOT NULL DEFAULT '1970-01-01 00:00:00',
    leased_by       VARCHAR(64)     NULL,
    fired_at        DATETIME(6)     NULL,
    last_error      TEXT            NULL,
    PRIMARY KEY (timer_set, timer_key),
    KEY scheduled_timers_due_idx (timer_set, fired_at, run_at, timer_key)
);


CREATE TABLE IF NOT EXISTS saga_instances (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    definition      VARCHAR(255) NOT NULL,
    status          VARCHAR(32) NOT NULL,
    current_step    INT NOT NULL DEFAULT 0,
    step_names      TEXT NOT NULL,
    state           BLOB,
    attempts        INT NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL,
    resume_status   VARCHAR(32) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    next_attempt    DATETIME(6) NOT NULL,
    claimed_until   DATETIME(6),
    KEY saga_instances_claim_idx (status, next_attempt, created_at, id),
    KEY saga_instances_status_idx (status, definition, id)
);


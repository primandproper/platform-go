CREATE TABLE IF NOT EXISTS operations (
    id               TEXT     PRIMARY KEY,
    kind             TEXT     NOT NULL,
    state            TEXT     NOT NULL,
    scope            TEXT     NOT NULL,
    request          BLOB,
    units_total      INTEGER,
    units_done       INTEGER  NOT NULL DEFAULT 0,
    progress_unit    TEXT     NOT NULL DEFAULT '',
    progress_count   INTEGER  NOT NULL DEFAULT 0,
    count_label      TEXT     NOT NULL DEFAULT '',
    progress_message TEXT     NOT NULL DEFAULT '',
    result_uri       TEXT     NOT NULL DEFAULT '',
    result_detail    BLOB,
    error_code       TEXT     NOT NULL DEFAULT '',
    error_message    TEXT     NOT NULL DEFAULT '',
    error_retryable  BOOLEAN  NOT NULL DEFAULT FALSE,
    revision         INTEGER  NOT NULL DEFAULT 1,
    attempts         INTEGER  NOT NULL DEFAULT 0,
    cancel_requested BOOLEAN  NOT NULL DEFAULT FALSE,
    created_at       DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%d %H:%M:%f', 'now')),
    last_updated_at  DATETIME,
    archived_at      DATETIME,
    started_at       DATETIME,
    finished_at      DATETIME,
    claimed_until    DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00.000'
);

CREATE INDEX IF NOT EXISTS operations_active_idx
    ON operations (created_at, claimed_until)
    WHERE state IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS operations_scope_idx
    ON operations (scope, kind, state, id);

CREATE INDEX IF NOT EXISTS operations_reap_idx
    ON operations (finished_at)
    WHERE state IN ('succeeded', 'failed', 'cancelled');


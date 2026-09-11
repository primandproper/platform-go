CREATE TABLE IF NOT EXISTS operations (
    id               TEXT        PRIMARY KEY,
    kind             TEXT        NOT NULL,
    state            TEXT        NOT NULL,
    scope            TEXT        NOT NULL,
    request          BYTEA,
    units_total      INTEGER,
    units_done       INTEGER     NOT NULL DEFAULT 0,
    progress_unit    TEXT        NOT NULL DEFAULT '',
    progress_count   BIGINT      NOT NULL DEFAULT 0,
    count_label      TEXT        NOT NULL DEFAULT '',
    progress_message TEXT        NOT NULL DEFAULT '',
    result_uri       TEXT        NOT NULL DEFAULT '',
    result_detail    BYTEA,
    error_code       TEXT        NOT NULL DEFAULT '',
    error_message    TEXT        NOT NULL DEFAULT '',
    error_retryable  BOOLEAN     NOT NULL DEFAULT FALSE,
    revision         BIGINT      NOT NULL DEFAULT 1,
    attempts         INTEGER     NOT NULL DEFAULT 0,
    cancel_requested BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at  TIMESTAMPTZ,
    archived_at      TIMESTAMPTZ,
    started_at       TIMESTAMPTZ,
    finished_at      TIMESTAMPTZ,
    claimed_until    TIMESTAMPTZ NOT NULL DEFAULT 'epoch'
);

CREATE INDEX IF NOT EXISTS operations_active_idx
    ON operations (created_at, claimed_until)
    WHERE state IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS operations_scope_idx
    ON operations (scope, kind, state, id);

CREATE INDEX IF NOT EXISTS operations_reap_idx
    ON operations (finished_at)
    WHERE state IN ('succeeded', 'failed', 'cancelled');


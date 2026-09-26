-- The Postgres table, spelled for SQLite. Every comment there applies here; what
-- this file says is only where the engine made the spelling differ.
--
-- The instants are text in one fixed shape, 'YYYY-MM-DD HH:MM:SS.SSS', and every
-- one of them is written by the server: strftime's %f is the only clock SQLite
-- has that reads finer than a second, and a fixed-width shape is what makes the
-- text comparisons every predicate here makes into time comparisons.
-- CURRENT_TIMESTAMP would be the second-granular spelling of the same clock, and
-- a lease rounded to a second is a lease that ends early; see the operations
-- package doc.
CREATE TABLE IF NOT EXISTS {{PREFIX}}operations (
    id               TEXT     PRIMARY KEY,
    kind             TEXT     NOT NULL,
    state            TEXT     NOT NULL,
    -- No DEFAULT, for the reason the Postgres schema gives.
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

-- The three indexes are the Postgres schema's, partial clauses included.
CREATE INDEX IF NOT EXISTS {{PREFIX}}operations_active_idx
    ON {{PREFIX}}operations (created_at, claimed_until)
    WHERE state IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS {{PREFIX}}operations_scope_idx
    ON {{PREFIX}}operations (scope, kind, state, id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}operations_reap_idx
    ON {{PREFIX}}operations (finished_at)
    WHERE state IN ('succeeded', 'failed', 'cancelled');

-- The Postgres table, spelled for MySQL. Every comment there applies here; what
-- this file says is only where the engine made the spelling differ.
--
-- id and kind are binary strings rather than VARCHAR, so that they compare byte
-- for byte as they do on the other two engines. MySQL's default collation — and
-- MariaDB's more so — folds case, accents and trailing spaces, and an id is the
-- idempotency seam WithID exists for: under VARCHAR two ids a caller derived as
-- different would collide here and nowhere else. id is sized to the work queue
-- key it is enqueued under, which is the bound it already had.
--
-- scope is VARCHAR(255) as every other scope column in this module is.
--
-- The three human-readable strings the store truncates to MaxMessageLength bytes
-- are VARCHAR(1024), which holds them and can carry a DEFAULT. The three it does
-- not bound are TEXT, and a TEXT column cannot carry a DEFAULT, so the insert
-- binds them rather than leaving them to the schema. request is a MEDIUMBLOB
-- because MaxRequestBytes is one byte more than a BLOB holds.
CREATE TABLE IF NOT EXISTS {{PREFIX}}operations (
    id               VARBINARY(512) NOT NULL PRIMARY KEY,
    kind             VARBINARY(128) NOT NULL,
    state            VARCHAR(16)    NOT NULL,
    -- No DEFAULT, for the reason the Postgres schema gives.
    scope            VARCHAR(255)   NOT NULL,
    request          MEDIUMBLOB,

    units_total      INT,
    units_done       INT            NOT NULL DEFAULT 0,
    progress_unit    VARCHAR(1024)  NOT NULL DEFAULT '',
    progress_count   BIGINT         NOT NULL DEFAULT 0,
    count_label      TEXT           NOT NULL,
    progress_message VARCHAR(1024)  NOT NULL DEFAULT '',

    result_uri       TEXT           NOT NULL,
    result_detail    BLOB,
    error_code       TEXT           NOT NULL,
    error_message    VARCHAR(1024)  NOT NULL DEFAULT '',
    error_retryable  BOOLEAN        NOT NULL DEFAULT FALSE,

    revision         BIGINT         NOT NULL DEFAULT 1,
    attempts         INT            NOT NULL DEFAULT 0,
    cancel_requested BOOLEAN        NOT NULL DEFAULT FALSE,

    -- CURRENT_TIMESTAMP(6) rather than the bare keyword, which is second-granular
    -- whatever the column declares.
    created_at       DATETIME(6)    NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at  DATETIME(6),
    archived_at      DATETIME(6),
    started_at       DATETIME(6),
    finished_at      DATETIME(6),
    claimed_until    DATETIME(6)    NOT NULL DEFAULT '1970-01-01 00:00:00',

    -- MySQL has no partial index, so where Postgres filters the active and the
    -- terminal rows into an index each, these lead with the state instead: the
    -- recovery sweep's two arms and the reaper's terminal states are equalities
    -- the rest of the key is ordered under.
    KEY {{PREFIX}}operations_active_idx (state, created_at, claimed_until),
    KEY {{PREFIX}}operations_scope_idx (scope, kind, state, id),
    KEY {{PREFIX}}operations_reap_idx (state, finished_at)
);

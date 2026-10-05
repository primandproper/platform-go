CREATE TABLE IF NOT EXISTS audit_log_entries (
    id            TEXT PRIMARY KEY,
    seq           BIGINT NOT NULL,
    scope         TEXT NOT NULL,
    recorded_at   TIMESTAMPTZ NOT NULL,
    event_type    TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL DEFAULT '',
    actor_id      TEXT NOT NULL,
    actor_type    TEXT NOT NULL DEFAULT '',
    actor_ip      TEXT NOT NULL DEFAULT '',
    change_set    BYTEA,
    metadata      BYTEA,
    prev_hash     TEXT NOT NULL DEFAULT '',
    hash          TEXT NOT NULL,
    actor_impersonator TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS audit_log_entries_chain_idx
    ON audit_log_entries (scope, seq);

CREATE INDEX IF NOT EXISTS audit_log_entries_scope_time_idx
    ON audit_log_entries (scope, recorded_at);

CREATE INDEX IF NOT EXISTS audit_log_entries_actor_idx
    ON audit_log_entries (actor_id, recorded_at);

CREATE INDEX IF NOT EXISTS audit_log_entries_resource_idx
    ON audit_log_entries (resource_type, resource_id, recorded_at);

CREATE INDEX IF NOT EXISTS audit_log_entries_impersonator_idx
    ON audit_log_entries (actor_impersonator, recorded_at);

CREATE TABLE IF NOT EXISTS audit_log_chains (
    scope               TEXT PRIMARY KEY,
    head_seq            BIGINT NOT NULL DEFAULT -1,
    head_hash           TEXT NOT NULL DEFAULT '',
    pruned_through_seq  BIGINT NOT NULL DEFAULT -1,
    pruned_through_hash TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at     TIMESTAMPTZ,
    archived_at         TIMESTAMPTZ
);


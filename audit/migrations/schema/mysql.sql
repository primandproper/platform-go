CREATE TABLE IF NOT EXISTS audit_log_entries (
    id            VARCHAR(64)  NOT NULL PRIMARY KEY,
    seq           BIGINT       NOT NULL,
    scope         VARCHAR(255) NOT NULL,
    recorded_at   DATETIME(6)  NOT NULL,
    event_type    VARCHAR(255) NOT NULL,
    resource_type VARCHAR(255) NOT NULL,
    resource_id   VARCHAR(255) NOT NULL DEFAULT '',
    actor_id      VARCHAR(255) NOT NULL,
    actor_type    VARCHAR(64)  NOT NULL DEFAULT '',
    actor_ip      VARCHAR(64)  NOT NULL DEFAULT '',
    change_set    LONGBLOB     NULL,
    metadata      LONGBLOB     NULL,
    prev_hash     VARCHAR(64)  NOT NULL DEFAULT '',
    hash          VARCHAR(64)  NOT NULL,
    actor_impersonator VARCHAR(255) NOT NULL DEFAULT '',
    UNIQUE KEY audit_log_entries_chain_idx (scope, seq),
    KEY audit_log_entries_scope_time_idx (scope, recorded_at),
    KEY audit_log_entries_actor_idx (actor_id, recorded_at),
    KEY audit_log_entries_resource_idx (resource_type, resource_id, recorded_at),
    KEY audit_log_entries_impersonator_idx (actor_impersonator, recorded_at)
);

CREATE TABLE IF NOT EXISTS audit_log_chains (
    scope               VARCHAR(255) NOT NULL PRIMARY KEY,
    head_seq            BIGINT       NOT NULL DEFAULT -1,
    head_hash           VARCHAR(64)  NOT NULL DEFAULT '',
    pruned_through_seq  BIGINT       NOT NULL DEFAULT -1,
    pruned_through_hash VARCHAR(64)  NOT NULL DEFAULT '',
    created_at          DATETIME(6)  NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at     DATETIME(6),
    archived_at         DATETIME(6)
);


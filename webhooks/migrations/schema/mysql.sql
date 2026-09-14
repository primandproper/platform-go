CREATE TABLE IF NOT EXISTS webhooks_endpoints (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    created_by      VARCHAR(255),
    name            VARCHAR(255) NOT NULL DEFAULT '',
    url             TEXT NOT NULL,
    content_type    VARCHAR(255) NOT NULL,
    secret_current  VARBINARY(512) NOT NULL,
    secret_previous VARBINARY(512),
    headers         BLOB NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6)
);

CREATE INDEX webhooks_endpoints_live_idx
    ON webhooks_endpoints (archived_at, created_at, id);

CREATE INDEX webhooks_endpoints_scope_idx
    ON webhooks_endpoints (scope, archived_at, id);

CREATE TABLE IF NOT EXISTS webhooks_subscriptions (
    id              VARCHAR(64) NOT NULL,
    endpoint_id     VARCHAR(64) NOT NULL,
    event_type      VARCHAR(255) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    PRIMARY KEY (endpoint_id, event_type),
    UNIQUE KEY webhooks_subscriptions_id_idx (id),
    CONSTRAINT webhooks_subscriptions_endpoint_fk
        FOREIGN KEY (endpoint_id) REFERENCES webhooks_endpoints (id) ON DELETE CASCADE
);

CREATE INDEX webhooks_subscriptions_event_idx
    ON webhooks_subscriptions (event_type, endpoint_id);

CREATE INDEX webhooks_subscriptions_endpoint_idx
    ON webhooks_subscriptions (endpoint_id, archived_at, id);

CREATE TABLE IF NOT EXISTS webhooks_deliveries (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    event_type      VARCHAR(255) NOT NULL,
    payload         LONGBLOB NOT NULL,
    ordering_key    VARCHAR(255) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6)
);

CREATE TABLE IF NOT EXISTS webhooks_dispatches (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    delivery_id     VARCHAR(64) NOT NULL,
    endpoint_id     VARCHAR(64) NOT NULL,
    ordering_key    VARCHAR(255) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    next_attempt    DATETIME(6) NOT NULL,
    claimed_until   DATETIME(6),
    claimed_by      VARCHAR(64),
    delivered_at    DATETIME(6),
    attempts        INT NOT NULL DEFAULT 0,
    last_error      TEXT,
    dead            BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE KEY webhooks_dispatches_pair_uniq (delivery_id, endpoint_id),
    CONSTRAINT webhooks_dispatches_delivery_fk
        FOREIGN KEY (delivery_id) REFERENCES webhooks_deliveries (id) ON DELETE CASCADE
);

CREATE INDEX webhooks_dispatches_claim_idx
    ON webhooks_dispatches (delivered_at, dead, next_attempt, created_at, id);

CREATE INDEX webhooks_dispatches_ordering_idx
    ON webhooks_dispatches (endpoint_id, ordering_key, delivered_at, dead, created_at, id);

CREATE INDEX webhooks_dispatches_reap_idx
    ON webhooks_dispatches (delivered_at, id);

CREATE TABLE IF NOT EXISTS webhooks_attempts (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    delivery_id     VARCHAR(64) NOT NULL,
    endpoint_id     VARCHAR(64) NOT NULL,
    attempt_count   INT NOT NULL,
    status_code     INT NOT NULL DEFAULT 0,
    error           TEXT,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6)
);

CREATE INDEX webhooks_attempts_delivery_idx
    ON webhooks_attempts (delivery_id, created_at, id);

CREATE INDEX webhooks_attempts_endpoint_idx
    ON webhooks_attempts (endpoint_id, created_at, id);


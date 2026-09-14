CREATE TABLE IF NOT EXISTS webhooks_endpoints (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    created_by      TEXT,
    name            TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    secret_current  BYTEA NOT NULL,
    secret_previous BYTEA,
    headers         BYTEA NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS webhooks_endpoints_live_idx
    ON webhooks_endpoints (created_at, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS webhooks_endpoints_scope_idx
    ON webhooks_endpoints (scope, id)
    WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS webhooks_subscriptions (
    id              TEXT NOT NULL,
    endpoint_id     TEXT NOT NULL REFERENCES webhooks_endpoints (id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    PRIMARY KEY (endpoint_id, event_type)
);

CREATE UNIQUE INDEX IF NOT EXISTS webhooks_subscriptions_id_idx
    ON webhooks_subscriptions (id);

CREATE INDEX IF NOT EXISTS webhooks_subscriptions_event_idx
    ON webhooks_subscriptions (event_type, endpoint_id);

CREATE INDEX IF NOT EXISTS webhooks_subscriptions_endpoint_idx
    ON webhooks_subscriptions (endpoint_id, id)
    WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS webhooks_deliveries (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         BYTEA NOT NULL,
    ordering_key    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS webhooks_dispatches (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL REFERENCES webhooks_deliveries (id) ON DELETE CASCADE,
    endpoint_id     TEXT NOT NULL,
    ordering_key    TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    next_attempt    TIMESTAMPTZ NOT NULL,
    claimed_until   TIMESTAMPTZ,
    claimed_by      TEXT,
    delivered_at    TIMESTAMPTZ,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    dead            BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (delivery_id, endpoint_id)
);

CREATE INDEX IF NOT EXISTS webhooks_dispatches_claim_idx
    ON webhooks_dispatches (next_attempt, created_at, id)
    WHERE delivered_at IS NULL AND dead = FALSE;

CREATE INDEX IF NOT EXISTS webhooks_dispatches_ordering_idx
    ON webhooks_dispatches (endpoint_id, ordering_key, created_at, id)
    WHERE delivered_at IS NULL AND dead = FALSE;

CREATE INDEX IF NOT EXISTS webhooks_dispatches_replay_idx
    ON webhooks_dispatches (delivery_id, endpoint_id);

CREATE INDEX IF NOT EXISTS webhooks_dispatches_reap_idx
    ON webhooks_dispatches (delivered_at)
    WHERE delivered_at IS NOT NULL;

CREATE TABLE IF NOT EXISTS webhooks_attempts (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL,
    endpoint_id     TEXT NOT NULL,
    attempt_count   INTEGER NOT NULL,
    status_code     INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    duration_ms     BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS webhooks_attempts_delivery_idx
    ON webhooks_attempts (delivery_id, created_at, id);

CREATE INDEX IF NOT EXISTS webhooks_attempts_endpoint_idx
    ON webhooks_attempts (endpoint_id, created_at, id);


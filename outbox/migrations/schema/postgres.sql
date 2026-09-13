CREATE TABLE IF NOT EXISTS outbox_messages (
    id            TEXT PRIMARY KEY,
    topic         TEXT NOT NULL,
    partition_key TEXT NOT NULL DEFAULT '',
    payload       BYTEA NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL,
    next_attempt  TIMESTAMPTZ NOT NULL,
    claimed_until TIMESTAMPTZ,
    claimed_by    TEXT,
    published_at  TIMESTAMPTZ,
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    quarantined   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS outbox_messages_claim_idx
    ON outbox_messages (next_attempt, created_at, id)
    WHERE published_at IS NULL AND quarantined = FALSE;

CREATE INDEX IF NOT EXISTS outbox_messages_ordering_idx
    ON outbox_messages (partition_key, created_at, id)
    WHERE published_at IS NULL AND quarantined = FALSE;

CREATE INDEX IF NOT EXISTS outbox_messages_reap_idx
    ON outbox_messages (published_at)
    WHERE published_at IS NOT NULL;


CREATE TABLE IF NOT EXISTS outbox_messages (
    id            VARCHAR(64)  NOT NULL PRIMARY KEY,
    topic         VARCHAR(255) NOT NULL,
    partition_key VARCHAR(255) NOT NULL DEFAULT '',
    payload       LONGBLOB     NOT NULL,
    created_at    DATETIME(6)  NOT NULL,
    next_attempt  DATETIME(6)  NOT NULL,
    claimed_until DATETIME(6)  NULL,
    claimed_by    VARCHAR(64)  NULL,
    published_at  DATETIME(6)  NULL,
    attempts      INT          NOT NULL DEFAULT 0,
    last_error    TEXT         NULL,
    quarantined   BOOLEAN      NOT NULL DEFAULT FALSE,
    KEY outbox_messages_claim_idx (published_at, quarantined, next_attempt, created_at),
    KEY outbox_messages_ordering_idx (partition_key, published_at, quarantined, created_at, id),
    KEY outbox_messages_reap_idx (published_at)
);


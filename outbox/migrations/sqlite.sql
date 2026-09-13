-- No convention triple, and that is deliberate. The relay sweeps published
-- messages, so this table is sized by the unpublished backlog rather than by
-- every message ever sent, and archived_at would either do nothing or defeat the
-- sweep. What a row's last write means here is already spelled by next_attempt,
-- claimed_until and published_at.
CREATE TABLE IF NOT EXISTS {{PREFIX}}outbox_messages (
    id            TEXT PRIMARY KEY,
    topic         TEXT NOT NULL,
    partition_key TEXT NOT NULL DEFAULT '',
    payload       BLOB NOT NULL,
    created_at    DATETIME NOT NULL,
    next_attempt  DATETIME NOT NULL,
    claimed_until DATETIME,
    claimed_by    TEXT,
    published_at  DATETIME,
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    quarantined   BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}outbox_messages_claim_idx
    ON {{PREFIX}}outbox_messages (next_attempt, created_at, id)
    WHERE published_at IS NULL AND quarantined = FALSE;

CREATE INDEX IF NOT EXISTS {{PREFIX}}outbox_messages_ordering_idx
    ON {{PREFIX}}outbox_messages (partition_key, created_at, id)
    WHERE published_at IS NULL AND quarantined = FALSE;

CREATE INDEX IF NOT EXISTS {{PREFIX}}outbox_messages_reap_idx
    ON {{PREFIX}}outbox_messages (published_at)
    WHERE published_at IS NOT NULL;

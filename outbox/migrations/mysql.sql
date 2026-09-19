-- MySQL has neither partial indexes nor CREATE INDEX IF NOT EXISTS, so the
-- indexes are declared inline and lead with the columns the claim predicate
-- filters on. They cover more rows than the Postgres equivalents; the reaper
-- keeps that bounded.
--
-- No convention triple, and that is deliberate. The relay sweeps published
-- messages, so this table is sized by the unpublished backlog rather than by
-- every message ever sent, and archived_at would either do nothing or defeat the
-- sweep. What a row's last write means here is already spelled by next_attempt,
-- claimed_until, published_at and quarantined_at.
CREATE TABLE IF NOT EXISTS {{PREFIX}}outbox_messages (
    id             VARCHAR(64)  NOT NULL PRIMARY KEY,
    topic          VARCHAR(255) NOT NULL,
    partition_key  VARCHAR(255) NOT NULL DEFAULT '',
    payload        LONGBLOB     NOT NULL,
    created_at     DATETIME(6)  NOT NULL,
    next_attempt   DATETIME(6)  NOT NULL,
    claimed_until  DATETIME(6)  NULL,
    claimed_by     VARCHAR(64)  NULL,
    published_at   DATETIME(6)  NULL,
    attempts       INT          NOT NULL DEFAULT 0,
    last_error     TEXT         NULL,
    quarantined_at DATETIME(6)  NULL,

    KEY {{PREFIX}}outbox_messages_claim_idx (published_at, quarantined_at, next_attempt, created_at),
    KEY {{PREFIX}}outbox_messages_ordering_idx (partition_key, published_at, quarantined_at, created_at, id),
    KEY {{PREFIX}}outbox_messages_reap_idx (published_at),
    KEY {{PREFIX}}outbox_messages_quarantine_idx (quarantined_at)
);

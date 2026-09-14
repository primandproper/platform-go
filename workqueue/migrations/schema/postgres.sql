CREATE TABLE IF NOT EXISTS work_queue_items (
    queue_name   TEXT        NOT NULL,
    item_key     TEXT        NOT NULL,
    priority     INTEGER     NOT NULL DEFAULT 0,
    attempts     INTEGER     NOT NULL DEFAULT 0,
    enqueued_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    lease_until  TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    leased_by    TEXT,
    completed_at TIMESTAMPTZ,
    last_error   TEXT,
    PRIMARY KEY (queue_name, item_key)
);

CREATE INDEX IF NOT EXISTS work_queue_items_claim_idx
    ON work_queue_items (queue_name, priority DESC, available_at, item_key)
    WHERE completed_at IS NULL;

CREATE INDEX IF NOT EXISTS work_queue_items_reap_idx
    ON work_queue_items (queue_name, completed_at)
    WHERE completed_at IS NOT NULL;


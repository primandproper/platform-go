CREATE TABLE IF NOT EXISTS work_queue_items (
    queue_name   VARBINARY(2048) NOT NULL,
    item_key     VARBINARY(512)  NOT NULL,
    priority     INT             NOT NULL DEFAULT 0,
    attempts     INT             NOT NULL DEFAULT 0,
    enqueued_at  DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    available_at DATETIME(6)     NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    lease_until  DATETIME(6)     NOT NULL DEFAULT '1970-01-01 00:00:00',
    leased_by    VARCHAR(64)     NULL,
    completed_at DATETIME(6)     NULL,
    last_error   TEXT            NULL,
    PRIMARY KEY (queue_name, item_key),
    KEY work_queue_items_claim_idx (queue_name, completed_at, priority DESC, available_at, item_key)
);


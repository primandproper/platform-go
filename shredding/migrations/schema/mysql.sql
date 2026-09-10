CREATE TABLE IF NOT EXISTS shredding_subject_keys (
    subject_type    VARCHAR(64) NOT NULL,
    subject_id      VARCHAR(255) NOT NULL,
    wrapped_key     BLOB,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    shredded_at     DATETIME(6),
    PRIMARY KEY (subject_type, subject_id)
);

CREATE INDEX shredding_subject_keys_shredded_idx
    ON shredding_subject_keys (shredded_at);


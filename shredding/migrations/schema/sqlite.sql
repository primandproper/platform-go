CREATE TABLE IF NOT EXISTS shredding_subject_keys (
    subject_type    TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    wrapped_key     BLOB,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME,
    shredded_at     DATETIME,
    PRIMARY KEY (subject_type, subject_id)
);

CREATE INDEX IF NOT EXISTS shredding_subject_keys_shredded_idx
    ON shredding_subject_keys (shredded_at)
    WHERE shredded_at IS NOT NULL;


CREATE TABLE IF NOT EXISTS shredding_subject_keys (
    subject_type    TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    wrapped_key     BYTEA,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    shredded_at     TIMESTAMPTZ,
    PRIMARY KEY (subject_type, subject_id)
);

CREATE INDEX IF NOT EXISTS shredding_subject_keys_shredded_idx
    ON shredding_subject_keys (shredded_at)
    WHERE shredded_at IS NOT NULL;


CREATE TABLE IF NOT EXISTS dataprivacy_requests (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    request_type    VARCHAR(32) NOT NULL,
    status          VARCHAR(32) NOT NULL,
    operation_id    VARCHAR(64) NOT NULL DEFAULT '',
    subject_id      VARCHAR(255) NOT NULL,
    subject_type    VARCHAR(64) NOT NULL DEFAULT '',
    subject_scope   VARCHAR(255) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    due_at          DATETIME(6) NOT NULL,
    expires_at      DATETIME(6),
    completed_at    DATETIME(6),
    artifact_ref    TEXT NOT NULL,
    artifact_bytes  BIGINT NOT NULL DEFAULT 0,
    deleted_rows    BIGINT NOT NULL DEFAULT 0,
    anonymized_rows BIGINT NOT NULL DEFAULT 0,
    failures        BLOB,
    retained        BLOB,
    last_error      TEXT,
    key_shredded_at DATETIME(6),
    KEY dataprivacy_requests_subject_idx
        (subject_id, subject_scope, created_at, id),
    KEY dataprivacy_requests_expiry_idx (status, expires_at),
    KEY dataprivacy_requests_status_due_idx (status, due_at),
    KEY dataprivacy_requests_reap_idx (completed_at, id)
);


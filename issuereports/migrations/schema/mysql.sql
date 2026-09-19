CREATE TABLE IF NOT EXISTS issue_reports (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    reporter        VARCHAR(64) NOT NULL,
    kind            VARCHAR(255) NOT NULL,
    details         TEXT NOT NULL,
    subject_type    VARCHAR(255) NOT NULL DEFAULT '',
    subject_id      VARCHAR(64) NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL,
    resolution      TEXT NOT NULL,
    closed_at       DATETIME(6),
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    KEY issue_reports_scope_idx (scope, archived_at, id),
    KEY issue_reports_status_idx (scope, archived_at, status, id),
    KEY issue_reports_reporter_idx (scope, archived_at, reporter, id),
    KEY issue_reports_subject_idx
        (scope, archived_at, subject_type, subject_id, id)
);


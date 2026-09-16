CREATE TABLE IF NOT EXISTS waitlists (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    closes_at       DATETIME(6) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    KEY waitlists_scope_idx (scope, archived_at, id),
    KEY waitlists_open_idx (scope, archived_at, closes_at, id)
);

CREATE TABLE IF NOT EXISTS waitlist_signups (
    id                VARCHAR(64) NOT NULL PRIMARY KEY,
    scope             VARCHAR(255) NOT NULL,
    waitlist_id       VARCHAR(64) NOT NULL,
    contact           VARCHAR(320) NOT NULL DEFAULT '',
    contact_digest    VARCHAR(128) NOT NULL,
    subject_type      VARCHAR(64) NOT NULL DEFAULT '',
    subject_id        VARCHAR(64) NOT NULL DEFAULT '',
    notes             VARCHAR(1024) NOT NULL DEFAULT '',
    status            VARCHAR(32) NOT NULL,
    status_changed_at DATETIME(6),
    created_at        DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at   DATETIME(6),
    archived_at       DATETIME(6),
    UNIQUE KEY waitlist_signups_contact_uniq (scope, waitlist_id, contact_digest),
    KEY waitlist_signups_waitlist_idx (scope, waitlist_id, archived_at, id),
    KEY waitlist_signups_subject_idx
        (scope, subject_type, subject_id, archived_at, id),
    CONSTRAINT waitlist_signups_fk
        FOREIGN KEY (waitlist_id) REFERENCES waitlists (id) ON DELETE CASCADE
);


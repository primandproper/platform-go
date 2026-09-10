CREATE TABLE IF NOT EXISTS uploads_objects (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    object_key      VARCHAR(512) NOT NULL,
    content_type    VARCHAR(255) NOT NULL,
    size_bytes      BIGINT NOT NULL,
    owner_id        VARCHAR(64) NOT NULL,
    belongs_to_type VARCHAR(64) NOT NULL DEFAULT '',
    belongs_to_id   VARCHAR(64) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY uploads_objects_key_uniq (scope, object_key)
);

CREATE INDEX uploads_objects_owner_idx
    ON uploads_objects (scope, archived_at, owner_id, id);

CREATE INDEX uploads_objects_subject_idx
    ON uploads_objects (scope, archived_at, belongs_to_type, belongs_to_id, id);


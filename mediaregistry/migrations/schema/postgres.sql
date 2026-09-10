CREATE TABLE IF NOT EXISTS uploads_objects (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    object_key      TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    owner_id        TEXT NOT NULL,
    belongs_to_type TEXT NOT NULL DEFAULT '',
    belongs_to_id   TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS uploads_objects_key_uniq
    ON uploads_objects (scope, object_key);

CREATE INDEX IF NOT EXISTS uploads_objects_owner_idx
    ON uploads_objects (scope, owner_id, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS uploads_objects_subject_idx
    ON uploads_objects (scope, belongs_to_type, belongs_to_id, id)
    WHERE archived_at IS NULL;


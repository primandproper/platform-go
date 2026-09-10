-- One row per object in storage: the key it lives at, what it is, how big it
-- is, who put it there, and which tenant it belongs to. See the Postgres schema
-- for why the table exists and why scope carries no default.
--
-- created_at is CURRENT_TIMESTAMP rather than a value the store binds, and on
-- this dialect that is what makes the column filterable. SQLite has no date
-- type, so a window comparison over it is lexicographic text, and
-- CURRENT_TIMESTAMP is what writes the UTC YYYY-MM-DD HH:MM:SS shape that
-- lexicographic order agrees with.
CREATE TABLE IF NOT EXISTS {{PREFIX}}uploads_objects (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    object_key      TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    size_bytes      BIGINT NOT NULL,
    owner_id        TEXT NOT NULL,
    belongs_to_type TEXT NOT NULL DEFAULT '',
    belongs_to_id   TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

-- An object key names one object, so it names one row — and the uniqueness
-- covers archived rows as well as live ones. See the Postgres schema.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_key_uniq
    ON {{PREFIX}}uploads_objects (scope, object_key);

CREATE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_owner_idx
    ON {{PREFIX}}uploads_objects (scope, owner_id, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_subject_idx
    ON {{PREFIX}}uploads_objects (scope, belongs_to_type, belongs_to_id, id)
    WHERE archived_at IS NULL;

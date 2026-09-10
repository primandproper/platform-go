-- One row per object in storage: the key it lives at, what it is, how big it
-- is, who put it there, and which tenant it belongs to. See the Postgres schema
-- for why the table exists and why scope carries no default.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for the
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT.
--
-- object_key is 512 rather than the 1024 characters S3 allows a key, and the
-- number is InnoDB's rather than a guess. The unique index below covers
-- (scope, object_key); under utf8mb4 that costs four bytes a character, so 255
-- and 512 together are 3,068 bytes against the 3,072-byte index key limit. A
-- deployment whose keys are longer widens both the column and the scope it
-- shares the index with, and nothing in this package truncates to the width, so
-- an over-long key is refused by the server rather than stored with its tail
-- silently gone.
CREATE TABLE IF NOT EXISTS {{PREFIX}}uploads_objects (
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
    UNIQUE KEY {{PREFIX}}uploads_objects_key_uniq (scope, object_key)
);

-- MySQL has no partial indexes, so unlike the Postgres schema these cover the
-- whole table and the predicate column leads. Both pages filter on archived_at,
-- so putting it in front keeps the index as selective as the partial clause is
-- elsewhere.
--
-- The uniqueness above covers archived rows as well as live ones in every
-- dialect, which is a decision rather than a MySQL concession — see the Postgres
-- schema for why archiving a row does not free its key.
CREATE INDEX {{PREFIX}}uploads_objects_owner_idx
    ON {{PREFIX}}uploads_objects (scope, archived_at, owner_id, id);

CREATE INDEX {{PREFIX}}uploads_objects_subject_idx
    ON {{PREFIX}}uploads_objects (scope, archived_at, belongs_to_type, belongs_to_id, id);

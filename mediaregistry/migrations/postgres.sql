-- One row per object in storage: the key it lives at, what it is, how big it
-- is, who put it there, and which tenant it belongs to.
--
-- The table is deliberately not the byte path. uploads writes the object and
-- hands back a key; this row is what makes that key answerable — "may this
-- caller read this object" is decided from owner_id and scope rather than from
-- the bucket, which is the whole reason the registry exists. A consumer without
-- it either serves objects public-by-key or grows a metadata table of its own,
-- and the grown one is where a tenant column gets forgotten.
--
-- scope is whose object this is: a reseller, a region, a product, or — as the
-- empty string — nobody. Every read of this table filters on it. It has no
-- default, which is the one place this schema departs from the module's habit
-- of defaulting a text column to the empty string: the empty string is a scope,
-- tenancy.Global(), and a column that supplied it for a write which did not
-- name one would hand the global scope to whoever forgot the column. NOT NULL
-- with nothing to fall back on makes that write fail instead.
--
-- size_bytes is what was actually stored, counted while the bytes went past,
-- rather than what a client claimed in a Content-Length header. A quota read
-- off claimed sizes is a quota that does not hold.
CREATE TABLE IF NOT EXISTS {{PREFIX}}uploads_objects (
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

-- An object key names one object, so it names one row — and the uniqueness
-- covers archived rows as well as live ones, with no partial clause.
--
-- That is a decision rather than a dialect limitation. Archival here is
-- metadata-only: the row is hidden, the object is still in the bucket until the
-- consumer's retention policy removes it, so freeing the key on archive would
-- let a second row claim a key whose bytes belong to the first. Two rows for one
-- object is exactly the drift this table exists to prevent, and the registry
-- would have no way to say which of them the bucket's bytes are.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_key_uniq
    ON {{PREFIX}}uploads_objects (scope, object_key);

-- Serves the owner page. Leading with scope is what keeps one tenant's page from
-- walking every other tenant's rows, and the trailing id is the cursor the walk
-- pages by.
CREATE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_owner_idx
    ON {{PREFIX}}uploads_objects (scope, owner_id, id)
    WHERE archived_at IS NULL;

-- Serves the "what is attached to this thing" page: the avatars on a user, the
-- receipts on an invoice. The pair is one key rather than two columns queried
-- independently — a belongs_to_id without its type is an id that means something
-- else in another table.
CREATE INDEX IF NOT EXISTS {{PREFIX}}uploads_objects_subject_idx
    ON {{PREFIX}}uploads_objects (scope, belongs_to_type, belongs_to_id, id)
    WHERE archived_at IS NULL;

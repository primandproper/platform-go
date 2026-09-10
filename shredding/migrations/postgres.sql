-- One table, one row per data subject, holding the only copy of that subject's
-- data key.
--
-- Read the backup policy note before deciding where this table lives. Shredding
-- a key here and restoring last week's snapshot of this table hands back the
-- wrapped key and, with it, everything that key opened. The same applies to a
-- point-in-time recovery window: the pre-shred tuple survives in the WAL until
-- it ages out, and this UPDATE does not overwrite bytes on disk. The guarantee
-- is that the key is unrecoverable once this table's own retention has passed,
-- which is why that retention has to be shorter than the retention of the data
-- the key protects.
CREATE TABLE IF NOT EXISTS {{PREFIX}}shredding_subject_keys (
    subject_type    TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    -- NULL once shredded. The row survives the key so the destruction is a
    -- record rather than an absence, and so a later read can say "destroyed"
    -- instead of minting a fresh key for somebody the system was told to forget.
    wrapped_key     BYTEA,
    -- The convention triple. created_at defaults server-side; last_updated_at is
    -- NULL until the shred rewrites the row, which is the only update this table
    -- takes. archived_at is here for the convention's sake and no statement in
    -- this package writes it: a key row is a record of destruction, and hiding
    -- one would hide the evidence the row exists to be.
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ,
    shredded_at     TIMESTAMPTZ,
    -- The pair, not the ID. A user and an account sharing an identifier are two
    -- subjects with two keys, and a primary key on the ID alone would silently
    -- make them one.
    PRIMARY KEY (subject_type, subject_id)
);

-- Serves "what was destroyed, and when" — the question a regulator asks and the
-- one an operator asks after a bad erasure job. Partial, because the answer only
-- ever concerns tombstones and the live rows are the overwhelming majority.
CREATE INDEX IF NOT EXISTS {{PREFIX}}shredding_subject_keys_shredded_idx
    ON {{PREFIX}}shredding_subject_keys (shredded_at)
    WHERE shredded_at IS NOT NULL;

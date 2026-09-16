-- scope is whose waitlists these are: a reseller, a region, a product, or — as
-- the empty string — nobody. Every read of every table here filters on it.
--
-- It has no default. The empty string is a scope, tenancy.Global(), and a
-- column that supplied it for a write which did not name one would hand out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists
-- to make unspellable in Go. NOT NULL with nothing to fall back on makes that
-- write fail instead. See the tenancy package.
--
-- The string columns here are VARCHAR where the Postgres schema says TEXT, for
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- generous and nothing in this package truncates to them, so a deployment that
-- needs more widens the column rather than losing the tail silently.
CREATE TABLE IF NOT EXISTS {{PREFIX}}waitlists (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    closes_at       DATETIME(6) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- MySQL has no partial indexes, so unlike the Postgres schema these cover
    -- the whole table and the predicate column leads: both pages filter on
    -- archived_at, so putting it in front keeps the index as selective as the
    -- partial clause is elsewhere.
    KEY {{PREFIX}}waitlists_scope_idx (scope, archived_at, id),
    KEY {{PREFIX}}waitlists_open_idx (scope, archived_at, closes_at, id)
);

-- closes_at is NOT NULL in every dialect — see the Postgres schema for why a
-- list that never closes is a list that is archived instead.

-- One person's place on one list. contact is the address the list writes to and
-- contact_digest is what the row is found by and what survives a withdrawal —
-- see the Postgres schema.
--
-- contact is 320 because that is the longest address RFC 5321 admits, and
-- contact_digest is 128 because SHA-256 renders as 64 hex characters and a
-- deployment that swaps the hasher for a wider one should not have to widen the
-- column too. The unique key below spans scope, waitlist_id and the digest —
-- 447 characters, 1788 bytes at four per character, against InnoDB's 3072-byte
-- key limit — so those three are the widths that cannot be raised freely.
CREATE TABLE IF NOT EXISTS {{PREFIX}}waitlist_signups (
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
    UNIQUE KEY {{PREFIX}}waitlist_signups_contact_uniq (scope, waitlist_id, contact_digest),

    -- Serves the queue: one list's live signups, walked by id.
    KEY {{PREFIX}}waitlist_signups_waitlist_idx (scope, waitlist_id, archived_at, id),

    -- Serves "which lists is this person on".
    KEY {{PREFIX}}waitlist_signups_subject_idx
        (scope, subject_type, subject_id, archived_at, id),
    CONSTRAINT {{PREFIX}}waitlist_signups_fk
        FOREIGN KEY (waitlist_id) REFERENCES {{PREFIX}}waitlists (id) ON DELETE CASCADE
);

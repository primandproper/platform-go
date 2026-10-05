-- No convention triple on this table, and each of the three columns is absent
-- for its own reason. recorded_at is folded into every entry's hash, computed
-- in Go before the INSERT and re-hashed on verification, so a database-assigned
-- creation stamp would store a value the hash does not cover and read back as
-- tampering; it is caller-assignable by design rather than a creation time, and
-- this package refuses to order by it because it is not monotonic. The table is
-- append-only by trigger besides, so last_updated_at and archived_at would be
-- columns no statement can write. audit_log_chains, below, is a different table
-- and carries the triple.
--
-- scope is whose entry this is: an account, an organization, a region, or — as
-- the empty string — nobody. It is also the chain's identity: entries are
-- positioned, hashed, and verified within one scope, so a row filed under the
-- wrong one is not a mislabeled row but a fork of somebody else's chain.
--
-- It has no default, and it is the one column here that departs from this
-- table's habit of defaulting a text column to the empty string. The neighbors
-- default because their empty string is an absence — an entry about no
-- particular resource, an actor whose type or address was not recorded — and a
-- write that omits one means exactly that. scope's empty string is not an
-- absence but a value, tenancy.Global(), so a default would hand the global
-- scope to a write that forgot the column — the mistake tenancy.Scope exists to
-- make unspellable in Go. NOT NULL with nothing to fall back on makes that write
-- fail instead. See the tenancy package.
CREATE TABLE IF NOT EXISTS {{PREFIX}}audit_log_entries (
    id            TEXT PRIMARY KEY,
    seq           BIGINT NOT NULL,
    scope         TEXT NOT NULL,
    recorded_at   DATETIME NOT NULL,
    event_type    TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL DEFAULT '',
    actor_id      TEXT NOT NULL,
    actor_type    TEXT NOT NULL DEFAULT '',
    actor_ip      TEXT NOT NULL DEFAULT '',
    change_set    BLOB,
    metadata      BLOB,
    prev_hash     TEXT NOT NULL DEFAULT '',
    hash          TEXT NOT NULL
);

-- See postgres.sql for what each of these is for; the definitions are the same.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_chain_idx
    ON {{PREFIX}}audit_log_entries (scope, seq);

CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_scope_time_idx
    ON {{PREFIX}}audit_log_entries (scope, recorded_at);

CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_actor_idx
    ON {{PREFIX}}audit_log_entries (actor_id, recorded_at);

CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_resource_idx
    ON {{PREFIX}}audit_log_entries (resource_type, resource_id, recorded_at);

-- Mutable, unlike the entries — appends advance the head, retention moves the
-- prune marker — so the exemption above does not reach it and it carries the
-- convention triple. postgres.sql says why archived_at is among them.
CREATE TABLE IF NOT EXISTS {{PREFIX}}audit_log_chains (
    scope               TEXT PRIMARY KEY,
    head_seq            BIGINT NOT NULL DEFAULT -1,
    head_hash           TEXT NOT NULL DEFAULT '',
    pruned_through_seq  BIGINT NOT NULL DEFAULT -1,
    pruned_through_hash TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at     DATETIME,
    archived_at         DATETIME
);

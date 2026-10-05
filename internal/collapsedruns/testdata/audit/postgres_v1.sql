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
    recorded_at   TIMESTAMPTZ NOT NULL,
    event_type    TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL DEFAULT '',
    actor_id      TEXT NOT NULL,
    actor_type    TEXT NOT NULL DEFAULT '',
    actor_ip      TEXT NOT NULL DEFAULT '',
    change_set    BYTEA,
    metadata      BYTEA,
    prev_hash     TEXT NOT NULL DEFAULT '',
    hash          TEXT NOT NULL
);

-- The chain's structural guarantee. Two writers racing on the same scope both
-- compute the same next position, and this index is what stops both from
-- committing: one transaction fails on the duplicate rather than both landing
-- and leaving a chain that forked. A fork is therefore not a thing Verify has
-- to detect, because it is not a thing the table can hold.
--
-- It also serves the chain-head read, which wants the highest seq in a scope.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_chain_idx
    ON {{PREFIX}}audit_log_entries (scope, seq);

-- Serves Verify's time-bounded walk and the retention sweep, both of which ask
-- for one scope's entries within a time range.
CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_scope_time_idx
    ON {{PREFIX}}audit_log_entries (scope, recorded_at);

-- "What did this principal do." Leading with the actor rather than the time is
-- what makes the answer a range scan instead of a filter over history.
CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_actor_idx
    ON {{PREFIX}}audit_log_entries (actor_id, recorded_at);

-- "What happened to this thing." Prefixed on resource_type so the same index
-- also answers the type-only question List supports.
CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_resource_idx
    ON {{PREFIX}}audit_log_entries (resource_type, resource_id, recorded_at);

-- One row per scope, holding that scope's chain head and how far retention has
-- pruned it.
--
-- The head is kept here rather than derived from the entries table on every
-- write because this row is what serializes concurrent writers: Record locks it
-- for the remainder of the caller's transaction, so the second writer waits and
-- then reads the head the first one committed. Locking the last entry row
-- instead cannot do that — the row a second writer would need to wait on is one
-- the first has not inserted yet.
--
-- It also survives pruning. Once retention deletes a scope's last surviving
-- entry there is nothing left to derive a head from, and a chain that restarted
-- at zero would collide with positions it had already used.
--
-- Unlike the entries it serializes, this row mutates — every append advances
-- the head and every retention pass moves the prune marker — so the entries'
-- exemption does not apply and the row carries the convention triple:
-- created_at is when the scope's chain began, and both writes stamp
-- last_updated_at. archived_at is written by no statement here — suppressing a
-- scope's tamper-evidence is not an operation this package offers — and is
-- carried because a conventional table has all three columns or none. Keyed on
-- scope rather than id, the table is conventional in schema only until the
-- generator can address a natural key.
CREATE TABLE IF NOT EXISTS {{PREFIX}}audit_log_chains (
    scope               TEXT PRIMARY KEY,
    head_seq            BIGINT NOT NULL DEFAULT -1,
    head_hash           TEXT NOT NULL DEFAULT '',
    pruned_through_seq  BIGINT NOT NULL DEFAULT -1,
    pruned_through_hash TEXT NOT NULL DEFAULT '',
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at     TIMESTAMPTZ,
    archived_at         TIMESTAMPTZ
);

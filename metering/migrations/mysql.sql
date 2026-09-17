-- Two tables, and the split is load-bearing rather than normalization for its
-- own sake.
--
-- The events table is the ingest ledger: one row per idempotency key per tenant,
-- which is what makes counting exactly-once. It is written once and never updated, and it
-- is the evidence behind an invoice when somebody disputes one.
--
-- The totals table is the aggregate the read path and the flusher use. It is
-- small — one row per tenant, subject, meter, and period — and it is the only thing
-- Consume locks. Deriving it from the events table on every read would be a
-- group-by over a table that grows with traffic, on the path this package exists
-- to keep cheap.
--
-- scope is whose data a row is: an account, an organization, a workspace, or —
-- as the empty string — nobody. Both tables carry it, every consumer read
-- filters on it, and it leads both primary keys: the dedupe and the aggregate
-- are per tenant, and a key that left it out would let one tenant's idempotency
-- key silence another tenant's usage, and would fold two tenants' usage of the
-- same meter into one invoice line. It has no default, and that is deliberate:
-- the empty string is a scope, tenancy.Global(), and a column that supplies it
-- for a write which did not name one hands the global scope to whoever forgot
-- the column. NOT NULL with nothing to fall back on makes that write fail
-- instead. See the tenancy package.
--
-- What reads across every scope is this package's own machinery and nothing
-- else: the flush claim drains the backlog for every tenant at once, and the
-- retention pass draws one horizon over all of them. Both say so on their own
-- methods, and neither answers a consumer read.
--
-- No convention triple here, and that is deliberate. A retention sweep deletes
-- these rows outright once they age past the window, so archived_at would either
-- do nothing or keep the table growing forever; recorded_at is the retention key
-- rather than a creation stamp, and the row is written once and never updated,
-- so there is no last mutation for last_updated_at to record. The totals table
-- below is the one meant to be kept, and it carries the triple.
CREATE TABLE IF NOT EXISTS {{PREFIX}}metering_events (
    scope           VARCHAR(255) NOT NULL,

    -- Dedupe is an INSERT that either takes or does not, decided by the database
    -- in one round trip, and durable for as long as the row is retained. A
    -- cache-backed dedupe would be correct only for as long as the cache held,
    -- and a billing period is longer than any cache TTL anybody sets.
    --
    -- The primary key is (scope, meter, idempotency_key), not the key alone.
    -- Callers are told to use a request ID as the idempotency key, and one
    -- request routinely feeds more than one meter — an API call that bills both
    -- a request count and a byte count. Keyed on the key alone the second
    -- meter's insert is silently deduped against the first, and the customer is
    -- under-billed for it forever. The scope leads it for the same reason one
    -- meter down: two tenants' request IDs are drawn from two sequences nobody
    -- reconciled, so a key shared between them would dedupe one tenant's usage
    -- against another's and bill neither.
    --
    -- VARCHAR(255) rather than TEXT because MySQL cannot index a TEXT column
    -- without a prefix length, and a prefix-indexed primary key would dedupe on
    -- the first N bytes — quietly discarding usage whose keys share a prefix.
    idempotency_key VARCHAR(255) NOT NULL,
    subject         VARCHAR(255) NOT NULL,
    meter           VARCHAR(64) NOT NULL,
    quantity        BIGINT NOT NULL,
    occurred_at     DATETIME(6) NOT NULL,
    recorded_at     DATETIME(6) NOT NULL,
    period_start    DATETIME(6) NOT NULL,
    dimensions      BLOB,

    PRIMARY KEY (scope, meter, idempotency_key),

    -- Serves the retention reap, which asks one question about time and nothing
    -- else, and the per-period event listing behind a usage breakdown.
    KEY {{PREFIX}}metering_events_period_idx
        (scope, subject, meter, period_start, occurred_at),
    KEY {{PREFIX}}metering_events_reap_idx (recorded_at)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}metering_totals (
    scope            VARCHAR(255) NOT NULL,
    subject          VARCHAR(255) NOT NULL,
    meter            VARCHAR(64) NOT NULL,
    period_start     DATETIME(6) NOT NULL,
    period_end       DATETIME(6) NOT NULL,
    aggregation      VARCHAR(32) NOT NULL,
    quantity         BIGINT NOT NULL DEFAULT 0,
    -- The event time of the newest record folded in. AggregationLast orders by
    -- it, so a record that arrives late does not displace a newer one — which is
    -- the whole difference between "last" and "most recently ingested".
    last_occurred_at DATETIME(6) NOT NULL,
    -- How much of quantity a flusher has pinned for the post it currently owes,
    -- and how much the provider has already been told about. The difference
    -- between the two is what the next post carries, and claiming it is what
    -- makes that amount survive a retry: the provider dedupes on a key derived
    -- from the sequence, so a second attempt that recomputed its delta from a
    -- quantity usage had moved in the meantime would post a larger amount under
    -- a key the provider already has, keep the first amount, and settle past
    -- the difference. Pinned at claim and released by the settle, the amount a
    -- key stands for cannot change while that key is outstanding.
    claimed_quantity BIGINT NOT NULL DEFAULT 0,
    flushed_quantity BIGINT NOT NULL DEFAULT 0,
    -- How many times the provider has been told. The sequence is the varying
    -- component of the provider-side idempotency key: a retried post reuses it
    -- and is a no-op, and a genuinely new post gets a fresh one.
    flush_sequence   INT NOT NULL DEFAULT 0,
    flush_attempts   INT NOT NULL DEFAULT 0,
    next_flush       DATETIME(6) NOT NULL,
    claimed_until    DATETIME(6),
    -- NOT NULL and no DEFAULT, unlike the other two, because MySQL takes no
    -- literal default on a TEXT column. Every row is opened by the same seed,
    -- which supplies the empty string — see metering/internal/queries — so the
    -- column holds what it holds elsewhere and the generated projection is a
    -- string on all three rather than a pointer here and a value there.
    last_error       TEXT NOT NULL,
    created_at       DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at  DATETIME(6),
    archived_at      DATETIME(6),
    PRIMARY KEY (scope, subject, meter, period_start),

    -- MySQL has no partial indexes, so unlike the Postgres schema this covers
    -- the whole table. next_flush leads because it is the only column of the
    -- claim predicate that is selective — the quantity > flushed_quantity
    -- comparison is between two columns and no index can serve it anywhere.
    KEY {{PREFIX}}metering_totals_flush_idx (next_flush, scope, subject, meter),

    -- Serves "what has this subject used lately", across meters.
    KEY {{PREFIX}}metering_totals_subject_idx (scope, subject, period_start, meter)
);

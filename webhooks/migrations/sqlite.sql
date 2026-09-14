-- Every table here carries the module's convention triple: created_at NOT NULL
-- with a server-side default, last_updated_at NULL until something changes the
-- row, archived_at NULL until the row is soft-deleted.
--
-- The endpoint is the only one of these a caller archives today. The delivery
-- side carries archived_at for the convention's sake — a table querygen can read
-- a shape from has all three or none — while the reaper still removes delivered
-- dispatches outright, which is what keeps the claim index sized by backlog.

-- scope is whose endpoint it is: an account, an organization, a workspace, or —
-- as the empty string — nobody. Every read of this table filters on it.
--
-- It has no default, deliberately. The empty string is a scope, tenancy.Global(),
-- and a column that supplies it for a write which did not name one hands out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists to
-- make unspellable in Go. NOT NULL with nothing to fall back on makes that write
-- fail instead. See the tenancy package.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webhooks_endpoints (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    created_by      TEXT,
    name            TEXT NOT NULL DEFAULT '',
    url             TEXT NOT NULL,
    content_type    TEXT NOT NULL,
    secret_current  BLOB NOT NULL,
    secret_previous BLOB,
    headers         BLOB NOT NULL,
    disabled        BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_endpoints_live_idx
    ON {{PREFIX}}webhooks_endpoints (created_at, id)
    WHERE archived_at IS NULL;

-- Serves the registry read, which is always one scope's page ordered by id, and
-- is available to the fan-out join's scope predicate. Leading with scope is what
-- keeps one tenant's page from walking every other tenant's rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_endpoints_scope_idx
    ON {{PREFIX}}webhooks_endpoints (scope, id)
    WHERE archived_at IS NULL;

-- Subscriptions are a join table rather than an array column on the endpoint so
-- that "who wants this event" is an index lookup instead of a scan over every
-- registered endpoint. Dispatch runs that query inside the caller's
-- transaction, so its cost is paid by the request that emitted the event.
--
-- The primary key is still the pair, because one endpoint subscribes to one
-- event type once. id is a surrogate beside it rather than in front of it: it is
-- what an API names when it is asked to retire one subscription, and it is what
-- an existing deployment can add in one ALTER without rebuilding the table's
-- identity. Re-subscribing to an archived event type revives that row and keeps
-- its id, so a link to a subscription does not rot.
--
-- The convention triple is here now, where it used to be deliberately absent.
-- Archiving one subscription is a thing a consumer's API does — its users retire
-- one event type without rewriting the set — and a row that can be soft-deleted
-- on its own has to be able to say when it was.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webhooks_subscriptions (
    id              TEXT NOT NULL,
    endpoint_id     TEXT NOT NULL REFERENCES {{PREFIX}}webhooks_endpoints (id) ON DELETE CASCADE,
    event_type      TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME,
    PRIMARY KEY (endpoint_id, event_type)
);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}webhooks_subscriptions_id_idx
    ON {{PREFIX}}webhooks_subscriptions (id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_subscriptions_event_idx
    ON {{PREFIX}}webhooks_subscriptions (event_type, endpoint_id);

-- Serves the paged read of one endpoint's subscriptions, which is cursor-ordered
-- on id. The event-type index above cannot: it leads with the column this query
-- does not filter on.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_subscriptions_endpoint_idx
    ON {{PREFIX}}webhooks_subscriptions (endpoint_id, id)
    WHERE archived_at IS NULL;

-- The delivery holds the payload once, however many subscribers it fans out to.
--
-- Its scope is whose event it was. It bounds the fan-out that produced the
-- dispatches below, and it is what the delivery log is read through — an attempt
-- carries no scope of its own, because the delivery it describes already has one.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webhooks_deliveries (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    event_type      TEXT NOT NULL,
    payload         BLOB NOT NULL,
    ordering_key    TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

-- One dispatch per (delivery, endpoint): the row the worker claims, retries,
-- and eventually gives up on. Per-endpoint attempt state lives here and nowhere
-- else, which is what lets four subscribers succeed on the first attempt while
-- a fifth is still backing off.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webhooks_dispatches (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL REFERENCES {{PREFIX}}webhooks_deliveries (id) ON DELETE CASCADE,
    endpoint_id     TEXT NOT NULL,
    ordering_key    TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME,
    next_attempt    DATETIME NOT NULL,
    claimed_until   DATETIME,
    -- The name of the claim that holds the lease. See the Postgres schema for
    -- why it is nullable.
    claimed_by      TEXT,
    delivered_at    DATETIME,
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT,
    dead            BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (delivery_id, endpoint_id)
);

-- The claim predicate only ever looks at undelivered, live rows, so the index
-- serving it excludes everything else. Without the partial clause this index
-- grows with total history rather than with backlog, and the worker slows down
-- as the table fills.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_dispatches_claim_idx
    ON {{PREFIX}}webhooks_dispatches (next_attempt, created_at, id)
    WHERE delivered_at IS NULL AND dead = FALSE;

-- Serves the NOT EXISTS enforcing per-key ordering. The leading column is
-- endpoint_id, not ordering_key: ordering is per (endpoint, key), so that a
-- subscriber which is timing out delays only its own queue for that key and
-- never another subscriber's. id is in the key because the predicate orders on
-- (created_at, id) — dispatches fanned out from one delivery share a timestamp
-- and are separated only by id.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_dispatches_ordering_idx
    ON {{PREFIX}}webhooks_dispatches (endpoint_id, ordering_key, created_at, id)
    WHERE delivered_at IS NULL AND dead = FALSE;

-- Serves Requeue, which names a (delivery, endpoint) pair.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_dispatches_replay_idx
    ON {{PREFIX}}webhooks_dispatches (delivery_id, endpoint_id);

-- Serves the reaper.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_dispatches_reap_idx
    ON {{PREFIX}}webhooks_dispatches (delivered_at)
    WHERE delivered_at IS NOT NULL;

-- The delivery log. Append-only, and deliberately not constrained by a foreign
-- key to the endpoint: an endpoint can be archived, and "what did we send them"
-- is asked most often after someone has been removed.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webhooks_attempts (
    id              TEXT PRIMARY KEY,
    delivery_id     TEXT NOT NULL,
    endpoint_id     TEXT NOT NULL,
    attempt_count   INTEGER NOT NULL,
    status_code     INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    duration_ms     INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_attempts_delivery_idx
    ON {{PREFIX}}webhooks_attempts (delivery_id, created_at, id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_attempts_endpoint_idx
    ON {{PREFIX}}webhooks_attempts (endpoint_id, created_at, id);

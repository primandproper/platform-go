-- Brings a schema created before subscriptions were rows up to the shape
-- sqlite.sql now creates. See the Upgrading section of this package's
-- documentation for what it is and is not.
--
-- SQLite has no ADD COLUMN IF NOT EXISTS, so the ALTERs are not re-runnable.
-- Run it once, through the migration runner that records which migrations have
-- run.

-- The endpoint's new metadata. created_by is nullable because it is optional:
-- NULL is "this application does not attribute endpoints to a principal", which
-- is different from the empty identifier, and the empty identifier is
-- tenancy.Global().
ALTER TABLE {{PREFIX}}webhooks_endpoints ADD COLUMN created_by TEXT;

ALTER TABLE {{PREFIX}}webhooks_endpoints ADD COLUMN name TEXT NOT NULL DEFAULT '';

-- id carries a DEFAULT here that a freshly created table does not, because
-- ADD COLUMN NOT NULL needs something to fill the rows that already exist. The
-- backfill below replaces every one of them, and nothing in this package ever
-- lets the default fire again: every insert names the column.
ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN id TEXT NOT NULL DEFAULT '';

-- The default is a literal rather than CURRENT_TIMESTAMP: SQLite refuses a
-- non-constant default on ADD COLUMN. It is written in the layout modernc's
-- driver stores a bound time.Time in, so a row that somehow escaped the backfill
-- below still reads back as a time rather than as a zero value. Every row is
-- backfilled, so none should.
ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN created_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00 +0000 UTC';

ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN last_updated_at DATETIME;

ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN archived_at DATETIME;

-- One identifier per existing subscription. They are random rather than derived
-- from the pair: an id derived from (endpoint_id, event_type) would be a second
-- encoding of the same two facts, and re-deriving it after a re-subscription
-- would hand out an identifier that had already been retired.
UPDATE {{PREFIX}}webhooks_subscriptions
    SET id = lower(hex(randomblob(16)))
    WHERE id = '';

-- A legacy subscription was written with its endpoint, so the endpoint's
-- creation time is the truthful answer for when it was subscribed — closer than
-- the moment this migration ran, which is what the column default would leave.
UPDATE {{PREFIX}}webhooks_subscriptions
    SET created_at = (
        SELECT e.created_at FROM {{PREFIX}}webhooks_endpoints AS e
        WHERE e.id = {{PREFIX}}webhooks_subscriptions.endpoint_id
    );

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}webhooks_subscriptions_id_idx
    ON {{PREFIX}}webhooks_subscriptions (id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}webhooks_subscriptions_endpoint_idx
    ON {{PREFIX}}webhooks_subscriptions (endpoint_id, id)
    WHERE archived_at IS NULL;

-- The dispatch's lease gained a holder: the name of the claim that took it,
-- which the two writes that report a delivery's outcome now present again so a
-- worker whose lease lapsed cannot retire or reschedule a dispatch somebody
-- else has since taken. Nullable, so every existing row reads as unheld, which
-- is what a row nobody has claimed since this ran in fact is.
ALTER TABLE {{PREFIX}}webhooks_dispatches ADD COLUMN claimed_by TEXT;

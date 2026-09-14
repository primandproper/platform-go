-- Brings a schema created before subscriptions were rows up to the shape
-- mysql.sql now creates. See the Upgrading section of this package's
-- documentation for what it is and is not.
--
-- MySQL has no ADD COLUMN IF NOT EXISTS and no CREATE INDEX IF NOT EXISTS, so
-- none of this is re-runnable. Run it once, through the migration runner that
-- records which migrations have run.

-- The endpoint's new metadata. created_by is nullable because it is optional:
-- NULL is "this application does not attribute endpoints to a principal", which
-- is different from the empty identifier, and the empty identifier is
-- tenancy.Global().
ALTER TABLE {{PREFIX}}webhooks_endpoints ADD COLUMN created_by VARCHAR(255);

ALTER TABLE {{PREFIX}}webhooks_endpoints ADD COLUMN name VARCHAR(255) NOT NULL DEFAULT '';

-- id carries a DEFAULT here that a freshly created table does not, because
-- ADD COLUMN NOT NULL needs something to fill the rows that already exist. The
-- backfill below replaces every one of them, and nothing in this package ever
-- lets the default fire again: every insert names the column.
ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN id VARCHAR(64) NOT NULL DEFAULT '';

ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN created_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);

ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN last_updated_at DATETIME(6);

ALTER TABLE {{PREFIX}}webhooks_subscriptions ADD COLUMN archived_at DATETIME(6);

-- One identifier per existing subscription. They are random rather than derived
-- from the pair: an id derived from (endpoint_id, event_type) would be a second
-- encoding of the same two facts, and re-deriving it after a re-subscription
-- would hand out an identifier that had already been retired.
UPDATE {{PREFIX}}webhooks_subscriptions
    SET id = REPLACE(UUID(), '-', '')
    WHERE id = '';

-- A legacy subscription was written with its endpoint, so the endpoint's
-- creation time is the truthful answer for when it was subscribed — closer than
-- the moment this migration ran, which is what the column default would leave.
UPDATE {{PREFIX}}webhooks_subscriptions AS s
    INNER JOIN {{PREFIX}}webhooks_endpoints AS e ON e.id = s.endpoint_id
    SET s.created_at = e.created_at;

CREATE UNIQUE INDEX {{PREFIX}}webhooks_subscriptions_id_idx
    ON {{PREFIX}}webhooks_subscriptions (id);

CREATE INDEX {{PREFIX}}webhooks_subscriptions_endpoint_idx
    ON {{PREFIX}}webhooks_subscriptions (endpoint_id, archived_at, id);

-- The dispatch's lease gained a holder: the name of the claim that took it,
-- which the two writes that report a delivery's outcome now present again so a
-- worker whose lease lapsed cannot retire or reschedule a dispatch somebody
-- else has since taken. Nullable, so every existing row reads as unheld, which
-- is what a row nobody has claimed since this ran in fact is.
ALTER TABLE {{PREFIX}}webhooks_dispatches ADD COLUMN claimed_by VARCHAR(64);

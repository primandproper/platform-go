-- Two tables, and the split is the difference between a fact about a person and
-- a fact about a handset. The Postgres schema carries the long form of why;
-- what is written here is what MySQL does differently.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for two
-- MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- generous rather than measured, and nothing in this package truncates to them,
-- so a consumer who needs more widens the column rather than losing the tail
-- silently.
CREATE TABLE IF NOT EXISTS {{PREFIX}}notifications_inbox (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    principal       VARCHAR(64) NOT NULL,
    topic           VARCHAR(255) NOT NULL,
    title           VARCHAR(255) NOT NULL,
    body            VARCHAR(2048) NOT NULL DEFAULT '',
    link            VARCHAR(2048) NOT NULL DEFAULT '',
    read_at         DATETIME(6),
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- MySQL has no partial indexes, so unlike the Postgres schema these cover
    -- the whole table and the predicate columns lead. Both reads filter on
    -- archived_at and the second on read_at as well, so putting them in front
    -- keeps the index as selective as the partial clause is elsewhere.
    KEY {{PREFIX}}notifications_inbox_principal_idx (scope, principal, archived_at, id),
    KEY {{PREFIX}}notifications_inbox_unread_idx
        (scope, principal, archived_at, read_at, id)
);

-- The device registry. No convention triple beyond created_at: a token is
-- revoked by its owner or invalidated by the provider, and either way the row
-- goes — see the Postgres schema.
--
-- token is 512 rather than the 2048 the message columns get: FCM registration
-- tokens run to a few hundred characters and APNs tokens to 64, and this column
-- is half of an index key, which MySQL bounds.
CREATE TABLE IF NOT EXISTS {{PREFIX}}notifications_devices (
    id           VARCHAR(64) NOT NULL PRIMARY KEY,
    scope        VARCHAR(255) NOT NULL,
    principal    VARCHAR(64) NOT NULL,
    platform     VARCHAR(32) NOT NULL,
    token        VARCHAR(512) NOT NULL,
    last_seen_at DATETIME(6) NOT NULL,
    created_at   DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),

    UNIQUE KEY {{PREFIX}}notifications_devices_token_uniq (platform, token),
    KEY {{PREFIX}}notifications_devices_principal_idx (scope, principal, id)
);

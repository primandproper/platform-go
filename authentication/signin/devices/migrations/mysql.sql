-- The table recording where each login was last renewed from. See postgres.sql
-- for what each column is and why; this file differs only in the types MySQL
-- needs. MySQL has no CREATE INDEX IF NOT EXISTS, so the two indexes are
-- declared inline.
--
-- The identifier columns are VARCHAR(255) because MySQL cannot key or index a
-- TEXT column without a prefix length. The three a client supplies are
-- VARCHAR(512), which is the bound the store truncates them to in Go before they
-- are bound — so the column never meets a value it would silently cut, and a
-- value the store let through is one MySQL stores whole.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_devices (
    scope         VARCHAR(255) NOT NULL,
    family_id     VARCHAR(255) NOT NULL,
    user_id       VARCHAR(255) NOT NULL,
    ip_address    VARCHAR(512) NOT NULL,
    user_agent    VARCHAR(512) NOT NULL,
    device_name   VARCHAR(512) NOT NULL,
    first_seen_at DATETIME(6)  NOT NULL,
    last_seen_at  DATETIME(6)  NOT NULL,
    expires_at    DATETIME(6)  NOT NULL,

    PRIMARY KEY (scope, family_id),
    INDEX {{PREFIX}}signin_devices_user_idx (scope, user_id),
    INDEX {{PREFIX}}signin_devices_expires_at_idx (expires_at)
);

-- The table recording where each login was last renewed from. See postgres.sql
-- for what each column is and why; this file differs only in the types SQLite
-- needs.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_devices (
    scope         TEXT NOT NULL,
    family_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    ip_address    TEXT NOT NULL,
    user_agent    TEXT NOT NULL,
    device_name   TEXT NOT NULL,
    first_seen_at DATETIME NOT NULL,
    last_seen_at  DATETIME NOT NULL,
    expires_at    DATETIME NOT NULL,

    PRIMARY KEY (scope, family_id)
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_devices_user_idx
    ON {{PREFIX}}signin_devices (scope, user_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_devices_expires_at_idx
    ON {{PREFIX}}signin_devices (expires_at);

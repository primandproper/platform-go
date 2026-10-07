CREATE TABLE IF NOT EXISTS signin_devices (
    scope         TEXT NOT NULL,
    family_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    ip_address    TEXT NOT NULL,
    user_agent    TEXT NOT NULL,
    device_name   TEXT NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL,
    last_seen_at  TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (scope, family_id)
);

CREATE INDEX IF NOT EXISTS signin_devices_user_idx
    ON signin_devices (scope, user_id);

CREATE INDEX IF NOT EXISTS signin_devices_expires_at_idx
    ON signin_devices (expires_at);


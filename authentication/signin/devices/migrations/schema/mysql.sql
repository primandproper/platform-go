CREATE TABLE IF NOT EXISTS signin_devices (
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
    INDEX signin_devices_user_idx (scope, user_id),
    INDEX signin_devices_expires_at_idx (expires_at)
);


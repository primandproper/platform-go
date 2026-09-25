CREATE TABLE IF NOT EXISTS signin_recovery_codes (
    scope     VARCHAR(255) NOT NULL,
    user_id   VARCHAR(255) NOT NULL,
    hash      VARCHAR(255) NOT NULL,
    issued_at DATETIME(6)  NOT NULL,
    used_at   DATETIME(6),
    PRIMARY KEY (scope, user_id, hash)
);


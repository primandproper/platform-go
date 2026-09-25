CREATE TABLE IF NOT EXISTS signin_recovery_codes (
    scope     TEXT NOT NULL,
    user_id   TEXT NOT NULL,
    hash      TEXT NOT NULL,
    issued_at DATETIME NOT NULL,
    used_at   DATETIME,
    PRIMARY KEY (scope, user_id, hash)
);


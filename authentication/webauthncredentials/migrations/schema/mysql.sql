CREATE TABLE IF NOT EXISTS webauthn_sessions (
    challenge    VARCHAR(255) NOT NULL PRIMARY KEY,
    session_data LONGBLOB     NOT NULL,
    expires_at   DATETIME(6)  NOT NULL,
    KEY webauthn_sessions_expires_at_idx (expires_at)
);


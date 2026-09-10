CREATE TABLE IF NOT EXISTS webauthn_sessions (
    challenge    TEXT PRIMARY KEY,
    session_data BLOB NOT NULL,
    expires_at   DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS webauthn_sessions_expires_at_idx
    ON webauthn_sessions (expires_at);


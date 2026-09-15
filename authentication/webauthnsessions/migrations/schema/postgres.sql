CREATE TABLE IF NOT EXISTS webauthn_sessions (
    challenge    TEXT PRIMARY KEY,
    session_data BYTEA NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS webauthn_sessions_expires_at_idx
    ON webauthn_sessions (expires_at);


-- No convention triple, and that is deliberate. Ceremony state lives for the
-- length of one registration or login, and a sweeper deletes it once expires_at
-- passes; a soft delete would keep rows nothing can ever read. There is no
-- update either — a challenge is written once and consumed once — so there is no
-- last mutation to record.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webauthn_sessions (
    challenge    TEXT PRIMARY KEY,
    session_data BLOB NOT NULL,
    expires_at   DATETIME NOT NULL
);

-- Serves the sweeper and Consume's expiry check. See postgres.sql.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webauthn_sessions_expires_at_idx
    ON {{PREFIX}}webauthn_sessions (expires_at);

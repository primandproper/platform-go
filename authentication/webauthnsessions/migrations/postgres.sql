-- No convention triple, and that is deliberate. Ceremony state lives for the
-- length of one registration or login, and a sweeper deletes it once expires_at
-- passes; a soft delete would keep rows nothing can ever read. There is no
-- update either — a challenge is written once and consumed once — so there is no
-- last mutation to record.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webauthn_sessions (
    challenge    TEXT PRIMARY KEY,
    session_data BYTEA NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL
);

-- Serves the sweeper, which is the only statement in this package that reads a
-- row it cannot name. Every other one is keyed by the challenge, which is the
-- primary key.
--
-- expires_at is also read by Consume, unlike the equivalent column in the
-- sessions schema: ceremony state has no second notion of liveness to compare
-- against, so a row that outlived its TTL is a row Consume must refuse rather
-- than one the sweeper will get to eventually.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webauthn_sessions_expires_at_idx
    ON {{PREFIX}}webauthn_sessions (expires_at);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_clients (
    id                          TEXT PRIMARY KEY,
    secret_hash                 TEXT NOT NULL,
    name                        TEXT NOT NULL,
    redirect_uris               TEXT NOT NULL,
    grant_types                 TEXT NOT NULL,
    response_types              TEXT NOT NULL,
    scopes                      TEXT NOT NULL,
    token_endpoint_auth_method  TEXT NOT NULL,
    created_at                  TIMESTAMPTZ NOT NULL,
    -- NULL is a registration that does not lapse, which is not the same as one
    -- that lapsed at the zero time. Every expiry predicate in this schema has
    -- to say so explicitly, and a NOT NULL column with a sentinel date would
    -- have made that a comparison against a magic number instead.
    expires_at                  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_clients_expires_at_idx
    ON {{PREFIX}}oauth2_clients (expires_at);

-- The three credential tables key on a hash, never on the credential. A dump of
-- this database therefore contains nothing that can be redeemed, which is the
-- property a map-backed store gets for free by dying with the process.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_authorization_codes (
    hash            TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL,
    -- The family this code will mint, decided at /authorize rather than at the
    -- redemption that uses it, so that a code presented a second time names the
    -- tokens the first presentation issued. Deliberately not indexed, unlike the
    -- family_id on the two token tables: nothing ever selects codes by family —
    -- the column is read back with the code its hash already found.
    family_id       TEXT NOT NULL,
    redirect_uri    TEXT NOT NULL,
    code_challenge  TEXT NOT NULL,
    nonce           TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    subject_claims  TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    resources       TEXT NOT NULL,
    issued_at       TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    -- NULL until the code is spent. The consuming UPDATE's predicate is
    -- `redeemed_at IS NULL AND expires_at > $now`, which is what makes one-time
    -- use and expiry a single atomic decision rather than a read followed by a
    -- write that another request can interleave with.
    redeemed_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_authorization_codes_expires_at_idx
    ON {{PREFIX}}oauth2_authorization_codes (expires_at);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_access_tokens (
    hash            TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL,
    family_id       TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    subject_claims  TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    audience        TEXT NOT NULL,
    issued_at       TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_access_tokens_expires_at_idx
    ON {{PREFIX}}oauth2_access_tokens (expires_at);

-- Serves the family revocation a detected token reuse triggers. Without it,
-- revoking a family scans every token this server has ever issued — at the one
-- moment where being slow is being unavailable.
CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_access_tokens_family_id_idx
    ON {{PREFIX}}oauth2_access_tokens (family_id);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens (
    hash            TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL,
    family_id       TEXT NOT NULL,
    subject_id      TEXT NOT NULL,
    subject_claims  TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    audience        TEXT NOT NULL,
    resources       TEXT NOT NULL,
    issued_at       TIMESTAMPTZ NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    redeemed_at     TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens_expires_at_idx
    ON {{PREFIX}}oauth2_refresh_tokens (expires_at);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens_family_id_idx
    ON {{PREFIX}}oauth2_refresh_tokens (family_id);

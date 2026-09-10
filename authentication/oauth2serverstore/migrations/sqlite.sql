-- See postgres.sql for what each column is and why the nullable timestamps are
-- nullable.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_clients (
    id                          TEXT PRIMARY KEY,
    secret_hash                 TEXT NOT NULL,
    name                        TEXT NOT NULL,
    redirect_uris               TEXT NOT NULL,
    grant_types                 TEXT NOT NULL,
    response_types              TEXT NOT NULL,
    scopes                      TEXT NOT NULL,
    token_endpoint_auth_method  TEXT NOT NULL,
    created_at                  DATETIME NOT NULL,
    expires_at                  DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_clients_expires_at_idx
    ON {{PREFIX}}oauth2_clients (expires_at);

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
    issued_at       DATETIME NOT NULL,
    expires_at      DATETIME NOT NULL,
    redeemed_at     DATETIME
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
    issued_at       DATETIME NOT NULL,
    expires_at      DATETIME NOT NULL,
    revoked_at      DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_access_tokens_expires_at_idx
    ON {{PREFIX}}oauth2_access_tokens (expires_at);

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
    issued_at       DATETIME NOT NULL,
    expires_at      DATETIME NOT NULL,
    redeemed_at     DATETIME,
    revoked_at      DATETIME
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens_expires_at_idx
    ON {{PREFIX}}oauth2_refresh_tokens (expires_at);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens_family_id_idx
    ON {{PREFIX}}oauth2_refresh_tokens (family_id);

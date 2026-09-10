-- MySQL has no CREATE INDEX IF NOT EXISTS, so every index here is declared
-- inline. See postgres.sql for what each column is and why the nullable
-- timestamps are nullable.
--
-- The key columns are VARCHAR(255) rather than TEXT because MySQL cannot index
-- a TEXT column without a prefix length, and the primary key is what every
-- statement in this package looks a row up by. A hex SHA-256 digest is 64
-- characters and a base64url client identifier is 43, so the declared width is
-- room to grow rather than a limit anyone will reach.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_clients (
    id                          VARCHAR(255) NOT NULL PRIMARY KEY,
    secret_hash                 VARCHAR(255) NOT NULL,
    name                        TEXT         NOT NULL,
    redirect_uris               TEXT         NOT NULL,
    grant_types                 TEXT         NOT NULL,
    response_types              TEXT         NOT NULL,
    scopes                      TEXT         NOT NULL,
    token_endpoint_auth_method  VARCHAR(64)  NOT NULL,
    created_at                  DATETIME(6)  NOT NULL,
    expires_at                  DATETIME(6)  NULL,

    KEY {{PREFIX}}oauth2_clients_expires_at_idx (expires_at)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_authorization_codes (
    hash            VARCHAR(255) NOT NULL PRIMARY KEY,
    client_id       VARCHAR(255) NOT NULL,
    -- The family this code will mint, decided at /authorize rather than at the
    -- redemption that uses it, so that a code presented a second time names the
    -- tokens the first presentation issued. Deliberately not indexed, unlike the
    -- family_id on the two token tables: nothing ever selects codes by family —
    -- the column is read back with the code its hash already found.
    family_id       VARCHAR(255) NOT NULL,
    redirect_uri    TEXT         NOT NULL,
    code_challenge  VARCHAR(255) NOT NULL,
    nonce           TEXT         NOT NULL,
    subject_id      VARCHAR(255) NOT NULL,
    subject_claims  TEXT         NOT NULL,
    scopes          TEXT         NOT NULL,
    resources       TEXT         NOT NULL,
    issued_at       DATETIME(6)  NOT NULL,
    expires_at      DATETIME(6)  NOT NULL,
    redeemed_at     DATETIME(6)  NULL,

    KEY {{PREFIX}}oauth2_authorization_codes_expires_at_idx (expires_at)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_access_tokens (
    hash            VARCHAR(255) NOT NULL PRIMARY KEY,
    client_id       VARCHAR(255) NOT NULL,
    family_id       VARCHAR(255) NOT NULL,
    subject_id      VARCHAR(255) NOT NULL,
    subject_claims  TEXT         NOT NULL,
    scopes          TEXT         NOT NULL,
    audience        TEXT         NOT NULL,
    issued_at       DATETIME(6)  NOT NULL,
    expires_at      DATETIME(6)  NOT NULL,
    revoked_at      DATETIME(6)  NULL,

    KEY {{PREFIX}}oauth2_access_tokens_expires_at_idx (expires_at),
    KEY {{PREFIX}}oauth2_access_tokens_family_id_idx (family_id)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_refresh_tokens (
    hash            VARCHAR(255) NOT NULL PRIMARY KEY,
    client_id       VARCHAR(255) NOT NULL,
    family_id       VARCHAR(255) NOT NULL,
    subject_id      VARCHAR(255) NOT NULL,
    subject_claims  TEXT         NOT NULL,
    scopes          TEXT         NOT NULL,
    audience        TEXT         NOT NULL,
    resources       TEXT         NOT NULL,
    issued_at       DATETIME(6)  NOT NULL,
    expires_at      DATETIME(6)  NOT NULL,
    redeemed_at     DATETIME(6)  NULL,
    revoked_at      DATETIME(6)  NULL,

    KEY {{PREFIX}}oauth2_refresh_tokens_expires_at_idx (expires_at),
    KEY {{PREFIX}}oauth2_refresh_tokens_family_id_idx (family_id)
);

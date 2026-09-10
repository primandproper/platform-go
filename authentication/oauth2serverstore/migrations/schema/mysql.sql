CREATE TABLE IF NOT EXISTS oauth2_clients (
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
    KEY oauth2_clients_expires_at_idx (expires_at)
);

CREATE TABLE IF NOT EXISTS oauth2_authorization_codes (
    hash            VARCHAR(255) NOT NULL PRIMARY KEY,
    client_id       VARCHAR(255) NOT NULL,
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
    KEY oauth2_authorization_codes_expires_at_idx (expires_at)
);

CREATE TABLE IF NOT EXISTS oauth2_access_tokens (
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
    KEY oauth2_access_tokens_expires_at_idx (expires_at),
    KEY oauth2_access_tokens_family_id_idx (family_id)
);

CREATE TABLE IF NOT EXISTS oauth2_refresh_tokens (
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
    KEY oauth2_refresh_tokens_expires_at_idx (expires_at),
    KEY oauth2_refresh_tokens_family_id_idx (family_id)
);


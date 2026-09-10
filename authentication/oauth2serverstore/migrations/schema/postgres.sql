CREATE TABLE IF NOT EXISTS oauth2_clients (
    id                          TEXT PRIMARY KEY,
    secret_hash                 TEXT NOT NULL,
    name                        TEXT NOT NULL,
    redirect_uris               TEXT NOT NULL,
    grant_types                 TEXT NOT NULL,
    response_types              TEXT NOT NULL,
    scopes                      TEXT NOT NULL,
    token_endpoint_auth_method  TEXT NOT NULL,
    created_at                  TIMESTAMPTZ NOT NULL,
    expires_at                  TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS oauth2_clients_expires_at_idx
    ON oauth2_clients (expires_at);

CREATE TABLE IF NOT EXISTS oauth2_authorization_codes (
    hash            TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL,
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
    redeemed_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS oauth2_authorization_codes_expires_at_idx
    ON oauth2_authorization_codes (expires_at);

CREATE TABLE IF NOT EXISTS oauth2_access_tokens (
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

CREATE INDEX IF NOT EXISTS oauth2_access_tokens_expires_at_idx
    ON oauth2_access_tokens (expires_at);

CREATE INDEX IF NOT EXISTS oauth2_access_tokens_family_id_idx
    ON oauth2_access_tokens (family_id);

CREATE TABLE IF NOT EXISTS oauth2_refresh_tokens (
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

CREATE INDEX IF NOT EXISTS oauth2_refresh_tokens_expires_at_idx
    ON oauth2_refresh_tokens (expires_at);

CREATE INDEX IF NOT EXISTS oauth2_refresh_tokens_family_id_idx
    ON oauth2_refresh_tokens (family_id);


CREATE TABLE IF NOT EXISTS oauth2_registered_clients (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    belongs_to_user TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    client_id       TEXT NOT NULL,
    secret_hash     TEXT NOT NULL,
    redirect_uris   TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS oauth2_registered_clients_client_id_uniq
    ON oauth2_registered_clients (client_id);

CREATE INDEX IF NOT EXISTS oauth2_registered_clients_scope_idx
    ON oauth2_registered_clients (scope, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS oauth2_registered_clients_owner_idx
    ON oauth2_registered_clients (scope, belongs_to_user, id)
    WHERE archived_at IS NULL;


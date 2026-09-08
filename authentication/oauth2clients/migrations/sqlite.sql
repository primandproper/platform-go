-- One table: an OAuth2 client somebody registered on purpose. See postgres.sql
-- for what each column is and why the nullable timestamps are nullable.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_registered_clients (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    belongs_to_user TEXT NOT NULL,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    client_id       TEXT NOT NULL,
    secret_hash     TEXT NOT NULL,
    redirect_uris   TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_client_id_uniq
    ON {{PREFIX}}oauth2_registered_clients (client_id);

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_scope_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_owner_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, belongs_to_user, id)
    WHERE archived_at IS NULL;

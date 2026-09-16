CREATE TABLE IF NOT EXISTS oauth2_registered_clients (
    id              VARCHAR(64)   NOT NULL PRIMARY KEY,
    scope           VARCHAR(255)  NOT NULL,
    belongs_to_user VARCHAR(64)   NOT NULL,
    name            TEXT          NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    client_id       VARCHAR(255)  NOT NULL,
    secret_hash     VARCHAR(255)  NOT NULL,
    redirect_uris   TEXT          NOT NULL,
    scopes          TEXT          NOT NULL,
    created_at      DATETIME(6)   NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6)   NULL,
    archived_at     DATETIME(6)   NULL,
    UNIQUE KEY oauth2_registered_clients_client_id_uniq (client_id),
    KEY oauth2_registered_clients_scope_idx (scope, archived_at, id),
    KEY oauth2_registered_clients_owner_idx
        (scope, archived_at, belongs_to_user, id)
);


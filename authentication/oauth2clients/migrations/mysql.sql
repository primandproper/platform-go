-- One table: an OAuth2 client somebody registered on purpose. The Postgres
-- schema carries the long form of why each column is here; what is written
-- below is what MySQL does differently.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for two
-- MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The three that
-- stay TEXT are the ones that are neither indexed nor defaulted — name, and the
-- two JSON lists, which hold as many addresses and scopes as a client
-- registers. description is the exception among the TEXT candidates: it takes a
-- DEFAULT, so it has to be a VARCHAR here.
--
-- The widths are generous rather than measured, and nothing in this package
-- truncates to them, so a consumer who needs more widens the column rather than
-- losing the tail silently.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_registered_clients (
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

    UNIQUE KEY {{PREFIX}}oauth2_registered_clients_client_id_uniq (client_id)
);

-- MySQL has no partial indexes, so unlike the Postgres schema these two cover
-- the whole table and archived_at leads the discriminating columns. Both reads
-- filter on it, so putting it in front keeps these as selective as the partial
-- clause is elsewhere.
--
-- The client_id lookup needs no index of its own: the UNIQUE KEY declared
-- inline above is one, and it is not partial in either schema for the reason
-- the Postgres file gives.
CREATE INDEX {{PREFIX}}oauth2_registered_clients_scope_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, archived_at, id);

CREATE INDEX {{PREFIX}}oauth2_registered_clients_owner_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, archived_at, belongs_to_user, id);

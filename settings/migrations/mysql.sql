-- scope is whose settings these are: a reseller, a region, a product, or — as
-- the empty string — nobody. Every read of every table here filters on it.
--
-- It has no default. The empty string is a scope, tenancy.Global(), and a
-- column that supplied it for a write which did not name one would hand out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists
-- to make unspellable in Go. NOT NULL with nothing to fall back on makes that
-- write fail instead. See the tenancy package.
--
-- A definition and the values stored against it share a scope. A deployment
-- with one catalog leaves every row global and gets exactly the behavior it
-- would have had without the column; a deployment whose tenants each define
-- their own settings gives each tenant's rows that tenant's scope. What the
-- schema does not offer is a global definition with per-tenant values, because
-- resolving one would take two scopes into a read whose whole guarantee is that
-- it takes one. See the settings package documentation.
--
-- The string columns here are VARCHAR where the Postgres schema says TEXT, for
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- generous and nothing in this package truncates to them, so a catalog that
-- needs more widens the column rather than losing the tail silently.
--
-- A setting's value is 512 rather than the 1024 a description gets, and the
-- narrower of the two is what every value column here takes: an enumerated
-- option is half of a primary key, InnoDB bounds a key at 3072 bytes, and
-- utf8mb4 charges four of them per character. Widening only the columns that
-- are not part of a key would leave a value a non-enumerated setting accepts
-- and an enumerated one cannot store — the same setting failing on the
-- definition of its own legal values.
CREATE TABLE IF NOT EXISTS {{PREFIX}}settings_definitions (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    name            VARCHAR(255) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    kind            VARCHAR(32) NOT NULL,
    default_value   VARCHAR(512),
    admin_only      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- The uniqueness covers archived rows as well as live ones in every
    -- dialect, which is a decision rather than a MySQL concession — see the
    -- Postgres schema for why a soft delete does not free a setting's name.
    UNIQUE KEY {{PREFIX}}settings_definitions_name_uniq (scope, name),

    -- MySQL has no partial indexes, so unlike the Postgres schema this covers
    -- the whole table and the predicate column leads. The catalog page filters
    -- on archived_at, so putting it in front keeps the index as selective as
    -- the partial clause is elsewhere.
    KEY {{PREFIX}}settings_definitions_scope_idx (scope, archived_at, id)
);

-- default_value is the one nullable column in this schema that could have been
-- NOT NULL DEFAULT '', and making it nullable is the whole of what "absence is
-- distinguishable from zero" means at the storage layer. A string setting whose
-- default is the empty string and a string setting with no default at all are
-- different definitions: the first answers every subject that has not chosen,
-- and the second answers none of them. Collapsed into one column value they
-- would be the same row.

-- The values a definition admits, one row each. An empty set means the
-- definition is not enumerated and any value of its kind is legal. A set rather
-- than a sequence — see the Postgres schema.
CREATE TABLE IF NOT EXISTS {{PREFIX}}settings_definition_options (
    definition_id VARCHAR(64) NOT NULL,
    value         VARCHAR(512) NOT NULL,
    PRIMARY KEY (definition_id, value),
    CONSTRAINT {{PREFIX}}settings_definition_options_fk
        FOREIGN KEY (definition_id) REFERENCES {{PREFIX}}settings_definitions (id) ON DELETE CASCADE
);

-- A subject's answer to one definition. subject_type and subject_id are two
-- columns rather than one composite string — see the Postgres schema.
CREATE TABLE IF NOT EXISTS {{PREFIX}}settings_values (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    definition_id   VARCHAR(64) NOT NULL,
    subject_type    VARCHAR(64) NOT NULL,
    subject_id      VARCHAR(64) NOT NULL,
    value           VARCHAR(512) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY {{PREFIX}}settings_values_subject_uniq (scope, subject_type, subject_id, definition_id),

    -- Serves the read that answers "who has overridden this setting", which is
    -- also the walk an edit to a definition's kind or enumeration is checked
    -- against.
    KEY {{PREFIX}}settings_values_definition_idx (scope, definition_id, archived_at, id),
    CONSTRAINT {{PREFIX}}settings_values_fk
        FOREIGN KEY (definition_id) REFERENCES {{PREFIX}}settings_definitions (id) ON DELETE CASCADE
);

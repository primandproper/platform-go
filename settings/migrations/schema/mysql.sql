CREATE TABLE IF NOT EXISTS settings_definitions (
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
    UNIQUE KEY settings_definitions_name_uniq (scope, name),
    KEY settings_definitions_scope_idx (scope, archived_at, id)
);

CREATE TABLE IF NOT EXISTS settings_definition_options (
    definition_id VARCHAR(64) NOT NULL,
    value         VARCHAR(512) NOT NULL,
    PRIMARY KEY (definition_id, value),
    CONSTRAINT settings_definition_options_fk
        FOREIGN KEY (definition_id) REFERENCES settings_definitions (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS settings_values (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    definition_id   VARCHAR(64) NOT NULL,
    subject_type    VARCHAR(128) NOT NULL,
    subject_id      VARCHAR(255) NOT NULL,
    value           VARCHAR(512) NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY settings_values_subject_uniq (scope, subject_type, subject_id, definition_id),
    KEY settings_values_definition_idx (scope, definition_id, archived_at, id),
    CONSTRAINT settings_values_fk
        FOREIGN KEY (definition_id) REFERENCES settings_definitions (id) ON DELETE CASCADE
);


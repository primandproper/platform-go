CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_roles (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}authz_roles_name_idx ON {{PREFIX}}authz_roles (name);

CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_permissions (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at DATETIME,
    archived_at     DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}authz_permissions_name_idx ON {{PREFIX}}authz_permissions (name);

-- This table and authz_role_hierarchy below hold mapping rows, and deliberately
-- carry none of the convention triple the tables they join do. Nothing lists,
-- filters or soft-deletes an edge independently of its role: revoking one
-- deletes the row, and archiving the role already hides every edge it owns. So
-- created_at, last_updated_at and archived_at here would be three columns no
-- statement in this package reads or writes.
CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_role_permissions (
    role_id       TEXT NOT NULL REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    permission_id TEXT NOT NULL REFERENCES {{PREFIX}}authz_permissions (id) ON DELETE CASCADE,
    PRIMARY KEY (role_id, permission_id)
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}authz_role_permissions_permission_idx
    ON {{PREFIX}}authz_role_permissions (permission_id);

CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_role_hierarchy (
    child_role_id  TEXT NOT NULL REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    parent_role_id TEXT NOT NULL REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    PRIMARY KEY (child_role_id, parent_role_id),
    CHECK (child_role_id <> parent_role_id)
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}authz_role_hierarchy_parent_idx
    ON {{PREFIX}}authz_role_hierarchy (parent_role_id);

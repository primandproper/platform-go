CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_roles (
    id              VARCHAR(191) NOT NULL PRIMARY KEY,
    name            VARCHAR(191) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY {{PREFIX}}authz_roles_name_idx (name)
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_permissions (
    id              VARCHAR(191) NOT NULL PRIMARY KEY,
    name            VARCHAR(191) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY {{PREFIX}}authz_permissions_name_idx (name)
);

-- This table and authz_role_hierarchy below hold mapping rows, and deliberately
-- carry none of the convention triple the tables they join do. Nothing lists,
-- filters or soft-deletes an edge independently of its role: revoking one
-- deletes the row, and archiving the role already hides every edge it owns. So
-- created_at, last_updated_at and archived_at here would be three columns no
-- statement in this package reads or writes.
CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_role_permissions (
    role_id       VARCHAR(191) NOT NULL,
    permission_id VARCHAR(191) NOT NULL,
    PRIMARY KEY (role_id, permission_id),
    KEY {{PREFIX}}authz_role_permissions_permission_idx (permission_id),
    CONSTRAINT {{PREFIX}}authz_role_permissions_role_fk
        FOREIGN KEY (role_id) REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT {{PREFIX}}authz_role_permissions_permission_fk
        FOREIGN KEY (permission_id) REFERENCES {{PREFIX}}authz_permissions (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS {{PREFIX}}authz_role_hierarchy (
    child_role_id  VARCHAR(191) NOT NULL,
    parent_role_id VARCHAR(191) NOT NULL,
    PRIMARY KEY (child_role_id, parent_role_id),
    KEY {{PREFIX}}authz_role_hierarchy_parent_idx (parent_role_id),
    CONSTRAINT {{PREFIX}}authz_role_hierarchy_child_fk
        FOREIGN KEY (child_role_id) REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT {{PREFIX}}authz_role_hierarchy_parent_fk
        FOREIGN KEY (parent_role_id) REFERENCES {{PREFIX}}authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT {{PREFIX}}authz_role_hierarchy_no_self CHECK (child_role_id <> parent_role_id)
);

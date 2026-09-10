CREATE TABLE IF NOT EXISTS authz_roles (
    id              VARCHAR(191) NOT NULL PRIMARY KEY,
    name            VARCHAR(191) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY authz_roles_name_idx (name)
);

CREATE TABLE IF NOT EXISTS authz_permissions (
    id              VARCHAR(191) NOT NULL PRIMARY KEY,
    name            VARCHAR(191) NOT NULL,
    description     VARCHAR(1024) NOT NULL DEFAULT '',
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),
    UNIQUE KEY authz_permissions_name_idx (name)
);

CREATE TABLE IF NOT EXISTS authz_role_permissions (
    role_id       VARCHAR(191) NOT NULL,
    permission_id VARCHAR(191) NOT NULL,
    PRIMARY KEY (role_id, permission_id),
    KEY authz_role_permissions_permission_idx (permission_id),
    CONSTRAINT authz_role_permissions_role_fk
        FOREIGN KEY (role_id) REFERENCES authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT authz_role_permissions_permission_fk
        FOREIGN KEY (permission_id) REFERENCES authz_permissions (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS authz_role_hierarchy (
    child_role_id  VARCHAR(191) NOT NULL,
    parent_role_id VARCHAR(191) NOT NULL,
    PRIMARY KEY (child_role_id, parent_role_id),
    KEY authz_role_hierarchy_parent_idx (parent_role_id),
    CONSTRAINT authz_role_hierarchy_child_fk
        FOREIGN KEY (child_role_id) REFERENCES authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT authz_role_hierarchy_parent_fk
        FOREIGN KEY (parent_role_id) REFERENCES authz_roles (id) ON DELETE CASCADE,
    CONSTRAINT authz_role_hierarchy_no_self CHECK (child_role_id <> parent_role_id)
);


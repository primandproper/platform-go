CREATE TABLE IF NOT EXISTS identity_users (
    id                                      VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                                   VARCHAR(255) NOT NULL,
    username                                VARCHAR(255) NOT NULL,
    email_address                           VARCHAR(320) NOT NULL,
    first_name                              VARCHAR(255) NOT NULL DEFAULT '',
    last_name                               VARCHAR(255) NOT NULL DEFAULT '',
    hashed_password                         VARCHAR(512) NOT NULL,
    requires_password_change                BOOLEAN NOT NULL DEFAULT FALSE,
    password_last_changed_at                DATETIME(6),
    two_factor_secret                       VARCHAR(255) NOT NULL DEFAULT '',
    two_factor_secret_verified_at           DATETIME(6),
    email_address_verified_at               DATETIME(6),
    email_address_verification_token_digest VARCHAR(255) NOT NULL DEFAULT '',
    account_status                          VARCHAR(32) NOT NULL,
    account_status_explanation              VARCHAR(1024) NOT NULL DEFAULT '',
    last_accepted_terms_of_service          DATETIME(6),
    last_accepted_privacy_policy            DATETIME(6),
    created_at                              DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at                         DATETIME(6),
    archived_at                             DATETIME(6),
    UNIQUE KEY identity_users_username_uniq (scope, username),
    UNIQUE KEY identity_users_email_uniq (scope, email_address),
    KEY identity_users_scope_idx (scope, archived_at, username, id),
    KEY identity_users_email_token_digest_idx
        (scope, email_address_verification_token_digest)
);

CREATE TABLE IF NOT EXISTS identity_user_roles (
    user_id VARCHAR(64) NOT NULL,
    role    VARCHAR(255) NOT NULL,
    PRIMARY KEY (user_id, role),
    KEY identity_user_roles_role_idx (role, user_id),
    CONSTRAINT identity_user_roles_fk
        FOREIGN KEY (user_id) REFERENCES identity_users (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS identity_accounts (
    id                              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope                           VARCHAR(255) NOT NULL,
    name                            VARCHAR(255) NOT NULL,
    owner_user_id                   VARCHAR(64) NOT NULL,
    billing_status                  VARCHAR(32) NOT NULL,
    subscription_plan_id            VARCHAR(255),
    payment_processor_customer_id   VARCHAR(255) NOT NULL DEFAULT '',
    last_payment_provider_synced_at DATETIME(6),
    address_line1                   VARCHAR(255) NOT NULL DEFAULT '',
    address_line2                   VARCHAR(255) NOT NULL DEFAULT '',
    address_city                    VARCHAR(255) NOT NULL DEFAULT '',
    address_state                   VARCHAR(255) NOT NULL DEFAULT '',
    address_postal_code             VARCHAR(32) NOT NULL DEFAULT '',
    address_country                 VARCHAR(255) NOT NULL DEFAULT '',
    address_phone                   VARCHAR(64) NOT NULL DEFAULT '',
    time_zone                       VARCHAR(64) NOT NULL DEFAULT '',
    created_at                      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at                 DATETIME(6),
    archived_at                     DATETIME(6),
    KEY identity_accounts_scope_idx (scope, archived_at, id),
    KEY identity_accounts_billing_idx
        (scope, archived_at, billing_status, last_payment_provider_synced_at)
);

CREATE TABLE IF NOT EXISTS identity_memberships (
    id                 VARCHAR(64) NOT NULL PRIMARY KEY,
    scope              VARCHAR(255) NOT NULL,
    belongs_to_user    VARCHAR(64) NOT NULL,
    belongs_to_account VARCHAR(64) NOT NULL,
    default_account    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at    DATETIME(6),
    archived_at        DATETIME(6),
    UNIQUE KEY identity_memberships_pair_uniq (belongs_to_user, belongs_to_account),
    KEY identity_memberships_user_idx
        (belongs_to_user, archived_at, default_account DESC, belongs_to_account),
    KEY identity_memberships_account_idx (belongs_to_account, archived_at, id),
    CONSTRAINT identity_memberships_user_fk
        FOREIGN KEY (belongs_to_user) REFERENCES identity_users (id) ON DELETE CASCADE,
    CONSTRAINT identity_memberships_account_fk
        FOREIGN KEY (belongs_to_account) REFERENCES identity_accounts (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS identity_membership_roles (
    membership_id VARCHAR(64) NOT NULL,
    role          VARCHAR(255) NOT NULL,
    PRIMARY KEY (membership_id, role),
    KEY identity_membership_roles_role_idx (role, membership_id),
    CONSTRAINT identity_membership_roles_fk
        FOREIGN KEY (membership_id) REFERENCES identity_memberships (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS identity_invitations (
    id                 VARCHAR(64) NOT NULL PRIMARY KEY,
    scope              VARCHAR(255) NOT NULL,
    belongs_to_account VARCHAR(64) NOT NULL,
    from_user          VARCHAR(64) NOT NULL,
    to_email           VARCHAR(320) NOT NULL,
    to_name            VARCHAR(255) NOT NULL DEFAULT '',
    to_user            VARCHAR(64),
    token_digest       VARCHAR(255) NOT NULL,
    status             VARCHAR(32) NOT NULL,
    note               VARCHAR(1024) NOT NULL DEFAULT '',
    status_note        VARCHAR(1024) NOT NULL DEFAULT '',
    expires_at         DATETIME(6) NOT NULL,
    created_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at    DATETIME(6),
    archived_at        DATETIME(6),
    KEY identity_invitations_email_idx (scope, to_email, status, archived_at, id),
    KEY identity_invitations_from_idx (scope, from_user, status, archived_at, id),
    KEY identity_invitations_account_idx (belongs_to_account, id),
    CONSTRAINT identity_invitations_account_fk
        FOREIGN KEY (belongs_to_account) REFERENCES identity_accounts (id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS identity_invitation_roles (
    invitation_id VARCHAR(64) NOT NULL,
    role          VARCHAR(255) NOT NULL,
    PRIMARY KEY (invitation_id, role),
    CONSTRAINT identity_invitation_roles_fk
        FOREIGN KEY (invitation_id) REFERENCES identity_invitations (id) ON DELETE CASCADE
);


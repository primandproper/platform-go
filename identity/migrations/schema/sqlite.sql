CREATE TABLE IF NOT EXISTS identity_users (
    id                                      TEXT PRIMARY KEY,
    scope                                   TEXT NOT NULL,
    username                                TEXT NOT NULL,
    display_name                            TEXT NOT NULL DEFAULT '',
    email_address                           TEXT NOT NULL,
    first_name                              TEXT NOT NULL DEFAULT '',
    last_name                               TEXT NOT NULL DEFAULT '',
    hashed_password                         TEXT NOT NULL,
    requires_password_change                BOOLEAN NOT NULL DEFAULT FALSE,
    password_last_changed_at                DATETIME,
    two_factor_secret                       TEXT NOT NULL DEFAULT '',
    two_factor_secret_verified_at           DATETIME,
    email_address_verified_at               DATETIME,
    email_address_verification_token_digest TEXT NOT NULL DEFAULT '',
    account_status                          TEXT NOT NULL,
    account_status_explanation              TEXT NOT NULL DEFAULT '',
    last_accepted_terms_of_service          DATETIME,
    last_accepted_privacy_policy            DATETIME,
    created_at                              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at                         DATETIME,
    archived_at                             DATETIME,
    last_indexed_at                         DATETIME
);

CREATE UNIQUE INDEX IF NOT EXISTS identity_users_username_uniq
    ON identity_users (scope, username);

CREATE UNIQUE INDEX IF NOT EXISTS identity_users_email_uniq
    ON identity_users (scope, email_address);

CREATE INDEX IF NOT EXISTS identity_users_scope_idx
    ON identity_users (scope, username, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS identity_users_email_token_digest_idx
    ON identity_users (scope, email_address_verification_token_digest)
    WHERE email_address_verification_token_digest <> '';

CREATE TABLE IF NOT EXISTS identity_user_roles (
    user_id TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
    role    TEXT NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE INDEX IF NOT EXISTS identity_user_roles_role_idx
    ON identity_user_roles (role, user_id);

CREATE TABLE IF NOT EXISTS identity_accounts (
    id                              TEXT PRIMARY KEY,
    scope                           TEXT NOT NULL,
    name                            TEXT NOT NULL,
    owner_user_id                   TEXT NOT NULL,
    billing_status                  TEXT NOT NULL,
    subscription_plan_id            TEXT,
    payment_processor_customer_id   TEXT NOT NULL DEFAULT '',
    last_payment_provider_synced_at DATETIME,
    address_line1                   TEXT NOT NULL DEFAULT '',
    address_line2                   TEXT NOT NULL DEFAULT '',
    address_city                    TEXT NOT NULL DEFAULT '',
    address_state                   TEXT NOT NULL DEFAULT '',
    address_postal_code             TEXT NOT NULL DEFAULT '',
    address_country                 TEXT NOT NULL DEFAULT '',
    address_phone                   TEXT NOT NULL DEFAULT '',
    time_zone                       TEXT NOT NULL DEFAULT '',
    created_at                      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at                 DATETIME,
    archived_at                     DATETIME
);

CREATE INDEX IF NOT EXISTS identity_accounts_scope_idx
    ON identity_accounts (scope, id)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS identity_accounts_billing_idx
    ON identity_accounts (scope, billing_status, last_payment_provider_synced_at)
    WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS identity_memberships (
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    belongs_to_user    TEXT NOT NULL REFERENCES identity_users (id) ON DELETE CASCADE,
    belongs_to_account TEXT NOT NULL REFERENCES identity_accounts (id) ON DELETE CASCADE,
    default_account    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at    DATETIME,
    archived_at        DATETIME,
    UNIQUE (belongs_to_user, belongs_to_account)
);

CREATE INDEX IF NOT EXISTS identity_memberships_user_idx
    ON identity_memberships (belongs_to_user, default_account DESC, belongs_to_account)
    WHERE archived_at IS NULL;

CREATE INDEX IF NOT EXISTS identity_memberships_account_idx
    ON identity_memberships (belongs_to_account, id)
    WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS identity_membership_roles (
    membership_id TEXT NOT NULL REFERENCES identity_memberships (id) ON DELETE CASCADE,
    role          TEXT NOT NULL,
    PRIMARY KEY (membership_id, role)
);

CREATE INDEX IF NOT EXISTS identity_membership_roles_role_idx
    ON identity_membership_roles (role, membership_id);

CREATE TABLE IF NOT EXISTS identity_invitations (
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    belongs_to_account TEXT NOT NULL REFERENCES identity_accounts (id) ON DELETE CASCADE,
    from_user          TEXT NOT NULL,
    to_email           TEXT NOT NULL,
    to_name            TEXT NOT NULL DEFAULT '',
    to_user            TEXT,
    token_digest       TEXT NOT NULL,
    status             TEXT NOT NULL,
    note               TEXT NOT NULL DEFAULT '',
    status_note        TEXT NOT NULL DEFAULT '',
    expires_at         DATETIME NOT NULL,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at    DATETIME,
    archived_at        DATETIME
);

CREATE INDEX IF NOT EXISTS identity_invitations_email_idx
    ON identity_invitations (scope, to_email, id)
    WHERE status = 'pending' AND archived_at IS NULL;

CREATE INDEX IF NOT EXISTS identity_invitations_from_idx
    ON identity_invitations (scope, from_user, id)
    WHERE status = 'pending' AND archived_at IS NULL;

CREATE INDEX IF NOT EXISTS identity_invitations_account_idx
    ON identity_invitations (belongs_to_account, id);

CREATE TABLE IF NOT EXISTS identity_invitation_roles (
    invitation_id TEXT NOT NULL REFERENCES identity_invitations (id) ON DELETE CASCADE,
    role          TEXT NOT NULL,
    PRIMARY KEY (invitation_id, role)
);


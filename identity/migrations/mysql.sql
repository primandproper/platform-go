-- Every key here is declared inline under its CREATE TABLE IF NOT EXISTS rather
-- than as a CREATE INDEX statement of its own, which is the module's rule for
-- this dialect and not this file's preference. internal/schemaconvention is
-- where it is written down and where it is checked.
--
-- The names are the ones the other two dialects give the same indexes. MySQL
-- scopes an index name to its table and would accept shorter ones, but the
-- prefix vetting in database/ddl measures the longest identifier a prefix
-- renders across all three bodies, and a name only two of them spell is a name
-- that check stops measuring here.

-- scope is whose directory this row belongs to: a reseller, a region, a
-- product, or — as the empty string — nobody. Every read of every table here
-- filters on it. It is deliberately NOT the account: accounts are rows in this
-- schema, and an application with one directory leaves the scope global and
-- gets exactly the behavior it would have had without the column.
--
-- It has no default. The empty string is a scope, tenancy.Global(), and a
-- column that supplies it for a write which did not name one hands out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists
-- to make unspellable in Go. NOT NULL with nothing to fall back on makes that
-- write fail instead. See the tenancy package.
--
-- The string columns here are VARCHAR where the Postgres schema says TEXT, for
-- two MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. The widths are
-- the standards' — 320 for an email address, 255 for a name or a handle — and
-- nothing in this package truncates to them, so a directory that needs more
-- widens the column rather than losing the tail silently.
--
-- email_address_verification_token_digest holds the digest of the token a
-- verification link carries, never the token itself. See postgres.sql for why,
-- and identity.SQLStore for where the hashing happens.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_users (
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

    -- The uniqueness covers archived rows as well as live ones in every
    -- dialect, which is a decision rather than a MySQL concession — see the
    -- Postgres schema for why a soft delete does not free a username.
    UNIQUE KEY {{PREFIX}}identity_users_username_uniq (scope, username),
    UNIQUE KEY {{PREFIX}}identity_users_email_uniq (scope, email_address),

    -- MySQL has no partial indexes, so unlike the Postgres schema these cover
    -- the whole table and the predicate column leads. The directory page and
    -- the username prefix search both filter on archived_at, so putting it in
    -- front keeps the index as selective as the partial clause is elsewhere.
    KEY {{PREFIX}}identity_users_scope_idx (scope, archived_at, username, id),
    KEY {{PREFIX}}identity_users_email_token_digest_idx
        (scope, email_address_verification_token_digest)
);

-- The roles a user holds outside any account: operator, support, service
-- administrator — what a consumer would otherwise keep in a user_roles table of
-- its own, which is the table that made this one necessary.
--
-- They are separate from membership roles rather than a membership in a
-- notional global account, because the two answer different questions and are
-- granted by different people. An operator's "support" role does not make them
-- a member of anybody's account, and a role set that conflated the two would
-- put them on every roster.
--
-- No convention triple, unlike every table this one hangs off. Nothing lists,
-- filters or soft-deletes a role grant independently of its parent: the grants
-- are rewritten wholesale when the parent's role set changes, and archiving the
-- parent already hides them. created_at, last_updated_at and archived_at here
-- would be three columns no statement in this package reads or writes.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_user_roles (
    user_id VARCHAR(64) NOT NULL,
    role    VARCHAR(255) NOT NULL,
    PRIMARY KEY (user_id, role),

    KEY {{PREFIX}}identity_user_roles_role_idx (role, user_id),
    CONSTRAINT {{PREFIX}}identity_user_roles_fk
        FOREIGN KEY (user_id) REFERENCES {{PREFIX}}identity_users (id) ON DELETE CASCADE
);

-- An account is an organization: what users belong to and what invoices are
-- addressed to. Its name is deliberately not unique — two unrelated
-- organizations may both be called Acme, and refusing the second registration
-- fails for a reason the registrant cannot act on.
--
-- owner_user_id is the one belongs-to column in this schema with no REFERENCES
-- clause, and the omission is the decision rather than the oversight it looks
-- like. Neither behavior an FK offers is the one this column wants: ON DELETE
-- CASCADE would destroy an organization, its invoices and every other member's
-- work because one member exercised a right to be forgotten, and RESTRICT would
-- refuse that erasure outright — a right-to-be-forgotten transaction spans
-- every domain and has to commit. What is left is a reference the application
-- keeps: identity refuses to archive a user who still owns a live account, and
-- documents that an erasure leaves this column naming an id that no longer
-- exists. See identity.Store's ArchiveUser and EraseUser.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_accounts (
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

    KEY {{PREFIX}}identity_accounts_scope_idx (scope, archived_at, id),
    KEY {{PREFIX}}identity_accounts_billing_idx
        (scope, archived_at, billing_status, last_payment_provider_synced_at)
);

-- A membership is a many-to-many with facts of its own, so it is a row rather
-- than an array column on either side.
--
-- The pair is unique across live and archived rows, which is why rejoining an
-- account revives the archived membership rather than writing a second one —
-- see Store.CreateMembership. Two rows for one pair would make "is this user a
-- member" a question with two answers, and the one a query returned would
-- depend on its ORDER BY.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_memberships (
    id                 VARCHAR(64) NOT NULL PRIMARY KEY,
    scope              VARCHAR(255) NOT NULL,
    belongs_to_user    VARCHAR(64) NOT NULL,
    belongs_to_account VARCHAR(64) NOT NULL,
    default_account    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at    DATETIME(6),
    archived_at        DATETIME(6),

    UNIQUE KEY {{PREFIX}}identity_memberships_pair_uniq (belongs_to_user, belongs_to_account),
    KEY {{PREFIX}}identity_memberships_user_idx
        (belongs_to_user, archived_at, default_account DESC, belongs_to_account),
    KEY {{PREFIX}}identity_memberships_account_idx (belongs_to_account, archived_at, id),
    CONSTRAINT {{PREFIX}}identity_memberships_user_fk
        FOREIGN KEY (belongs_to_user) REFERENCES {{PREFIX}}identity_users (id) ON DELETE CASCADE,
    CONSTRAINT {{PREFIX}}identity_memberships_account_fk
        FOREIGN KEY (belongs_to_account) REFERENCES {{PREFIX}}identity_accounts (id) ON DELETE CASCADE
);

-- Roles are a join table rather than a list in a column, for the reason
-- webhook subscriptions are: "who holds this role" is then an index lookup
-- instead of a scan with a string match over every membership in the
-- deployment. Nothing in this package's Store asks that question today, and the
-- table is what makes it possible to add without a migration that has to
-- rewrite every row.
--
-- No convention triple, unlike every table this one hangs off. Nothing lists,
-- filters or soft-deletes a role grant independently of its parent: the grants
-- are rewritten wholesale when the parent's role set changes, and archiving the
-- parent already hides them. created_at, last_updated_at and archived_at here
-- would be three columns no statement in this package reads or writes.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_membership_roles (
    membership_id VARCHAR(64) NOT NULL,
    role          VARCHAR(255) NOT NULL,
    PRIMARY KEY (membership_id, role),

    KEY {{PREFIX}}identity_membership_roles_role_idx (role, membership_id),
    CONSTRAINT {{PREFIX}}identity_membership_roles_fk
        FOREIGN KEY (membership_id) REFERENCES {{PREFIX}}identity_memberships (id) ON DELETE CASCADE
);

-- An invitation is addressed to an email address rather than to a user,
-- because the common case is inviting somebody who has not registered yet.
-- to_user is filled in on acceptance, which is the first moment there is a user
-- to name.
--
-- The two notes are two columns because they are written by two people at two
-- moments. note is the sender's message, written once at creation and rendered
-- into the invite email; status_note is why the answer went the way it did,
-- written by whoever answered. One column would mean the reply erasing the
-- message it was replying to.
--
-- token_digest holds the digest of the invitation's token, never the token
-- itself. See postgres.sql.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_invitations (
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

    KEY {{PREFIX}}identity_invitations_email_idx (scope, to_email, status, archived_at, id),
    KEY {{PREFIX}}identity_invitations_from_idx (scope, from_user, status, archived_at, id),
    KEY {{PREFIX}}identity_invitations_account_idx (belongs_to_account, id),
    CONSTRAINT {{PREFIX}}identity_invitations_account_fk
        FOREIGN KEY (belongs_to_account) REFERENCES {{PREFIX}}identity_accounts (id) ON DELETE CASCADE
);

-- The roles an invitation promises, fixed at invitation time so that what
-- somebody was invited to is what they get. Same shape, and the same reason, as
-- the membership roles above.
--
-- No convention triple, unlike every table this one hangs off. Nothing lists,
-- filters or soft-deletes a role grant independently of its parent: the grants
-- are rewritten wholesale when the parent's role set changes, and archiving the
-- parent already hides them. created_at, last_updated_at and archived_at here
-- would be three columns no statement in this package reads or writes.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_invitation_roles (
    invitation_id VARCHAR(64) NOT NULL,
    role          VARCHAR(255) NOT NULL,
    PRIMARY KEY (invitation_id, role),
    CONSTRAINT {{PREFIX}}identity_invitation_roles_fk
        FOREIGN KEY (invitation_id) REFERENCES {{PREFIX}}identity_invitations (id) ON DELETE CASCADE
);

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
--
-- username is the lookup column, folded to lower case by the store on write and
-- on every lookup; username_display is the spelling as given, read by nothing.
-- See postgres.sql for why, and identity's package documentation under
-- "Handles are folded".
-- email_address_verification_token_digest holds the digest of the token a
-- verification link carries, never the token itself. See postgres.sql for why,
-- and identity.SQLStore for where the hashing happens.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_users (
    id                                      TEXT PRIMARY KEY,
    scope                                   TEXT NOT NULL,
    username                                TEXT NOT NULL,
    username_display                        TEXT NOT NULL DEFAULT '',
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
    archived_at                             DATETIME
);

-- Usernames and email addresses are unique per directory, and the uniqueness
-- covers archived rows as well as live ones — no partial clause.
--
-- That is a decision rather than a limitation of the dialects. Freeing a
-- username when its owner is soft-deleted means a later registrant can take it,
-- and every audit row, every webhook payload, and every support ticket naming
-- that handle then refers to two different people with nothing in the data
-- saying where one stops. A directory that genuinely wants the handle back
-- erases the user (Store.EraseUser), which removes the row and the claim
-- together — the deliberate act, rather than a side effect of a soft delete.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}identity_users_username_uniq
    ON {{PREFIX}}identity_users (scope, username);

CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}identity_users_email_uniq
    ON {{PREFIX}}identity_users (scope, email_address);

-- Serves the directory page and the username prefix search, both of which are
-- one scope ordered by username. Leading with scope is what keeps one
-- directory's page from walking every other directory's rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_users_scope_idx
    ON {{PREFIX}}identity_users (scope, username, id)
    WHERE archived_at IS NULL;

-- Serves the verification-link read, which looks the digest up rather than the
-- token a recipient presented. Partial on the digest being present, so the index
-- holds only the users with an outstanding link rather than one row per user in
-- the directory — the column is cleared when a link is used.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_users_email_token_digest_idx
    ON {{PREFIX}}identity_users (scope, email_address_verification_token_digest)
    WHERE email_address_verification_token_digest <> '';

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
    user_id TEXT NOT NULL REFERENCES {{PREFIX}}identity_users (id) ON DELETE CASCADE,
    role    TEXT NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_user_roles_role_idx
    ON {{PREFIX}}identity_user_roles (role, user_id);

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

CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_accounts_scope_idx
    ON {{PREFIX}}identity_accounts (scope, id)
    WHERE archived_at IS NULL;

-- Serves the reconciliation job, which orders by when an account last synced so
-- that one which never has sorts first.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_accounts_billing_idx
    ON {{PREFIX}}identity_accounts (scope, billing_status, last_payment_provider_synced_at)
    WHERE archived_at IS NULL;

-- A membership is a many-to-many with facts of its own, so it is a row rather
-- than an array column on either side.
--
-- The pair is unique across live and archived rows, which is why rejoining an
-- account revives the archived membership rather than writing a second one —
-- see Store.CreateMembership. Two rows for one pair would make "is this user a
-- member" a question with two answers, and the one a query returned would
-- depend on its ORDER BY.
CREATE TABLE IF NOT EXISTS {{PREFIX}}identity_memberships (
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    belongs_to_user    TEXT NOT NULL REFERENCES {{PREFIX}}identity_users (id) ON DELETE CASCADE,
    belongs_to_account TEXT NOT NULL REFERENCES {{PREFIX}}identity_accounts (id) ON DELETE CASCADE,
    default_account    BOOLEAN NOT NULL DEFAULT FALSE,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_updated_at    DATETIME,
    archived_at        DATETIME,
    UNIQUE (belongs_to_user, belongs_to_account)
);

-- Serves the read every authenticated request makes: this user's live
-- memberships, default first.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_memberships_user_idx
    ON {{PREFIX}}identity_memberships (belongs_to_user, default_account DESC, belongs_to_account)
    WHERE archived_at IS NULL;

-- Serves the account roster.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_memberships_account_idx
    ON {{PREFIX}}identity_memberships (belongs_to_account, id)
    WHERE archived_at IS NULL;

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
    membership_id TEXT NOT NULL REFERENCES {{PREFIX}}identity_memberships (id) ON DELETE CASCADE,
    role          TEXT NOT NULL,
    PRIMARY KEY (membership_id, role)
);

CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_membership_roles_role_idx
    ON {{PREFIX}}identity_membership_roles (role, membership_id);

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
    id                 TEXT PRIMARY KEY,
    scope              TEXT NOT NULL,
    belongs_to_account TEXT NOT NULL REFERENCES {{PREFIX}}identity_accounts (id) ON DELETE CASCADE,
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

-- Serves what a newly registered user is shown: the pending invitations
-- addressed to them. Partial on pending, because an answered invitation is
-- never read this way and an application accumulates far more of those.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_invitations_email_idx
    ON {{PREFIX}}identity_invitations (scope, to_email, id)
    WHERE status = 'pending' AND archived_at IS NULL;

-- Serves the sender looking at what they have sent.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_invitations_from_idx
    ON {{PREFIX}}identity_invitations (scope, from_user, id)
    WHERE status = 'pending' AND archived_at IS NULL;

-- Serves the account's own list of outstanding invitations, and the cascade
-- when an account is archived.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_invitations_account_idx
    ON {{PREFIX}}identity_invitations (belongs_to_account, id);

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
    invitation_id TEXT NOT NULL REFERENCES {{PREFIX}}identity_invitations (id) ON DELETE CASCADE,
    role          TEXT NOT NULL,
    PRIMARY KEY (invitation_id, role)
);

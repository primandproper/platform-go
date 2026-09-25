-- One row per grant a third party gave this deployment: the tokens a provider
-- handed over when somebody consented, for one subject, at one provider.
--
-- It is the other direction from authentication/oauth2serverstore. Those tables
-- are this deployment acting as the authorization server; this one is this
-- deployment acting as the client, holding a standing credential to somebody
-- else's account.
--
-- scope is whose grant this is, and like every scope column in the module it has
-- no default: the empty string is tenancy.Global(), and a column that supplied
-- it for a write which did not name one would hand the global scope to whoever
-- forgot the column.
--
-- subject is the consumer's own identifier for who consented — a tenant, a user,
-- a studio — and deliberately opaque here. provider is a label the consumer
-- chooses ("google"), and provider_account_id is the provider's own name for the
-- account the tokens reach, which is what a settings page shows and what a
-- privacy export has to say.
--
-- access_token and refresh_token are ciphertext, never the token. The store seals
-- each under the consumer's encryptor with (scope, subject, provider) and the
-- column's name as the associated data, so a ciphertext copied into another row,
-- or from one column into the other, fails to open rather than quietly working.
-- A revoked row holds neither: revocation empties both columns in the statement
-- that records it.
--
-- archived_at is when the grant was revoked, and revocation_reason says by whom:
-- 'revoked' when the consumer asked, 'invalid_grant' when the provider refused a
-- refresh. The row stays, because "this account was connected here and on this
-- date it stopped being" is a question a support desk asks; a new consent
-- replaces it outright.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_grants (
    id                      TEXT PRIMARY KEY,
    scope                   TEXT NOT NULL,
    subject                 TEXT NOT NULL,
    provider                TEXT NOT NULL,
    provider_account_id     TEXT NOT NULL DEFAULT '',
    granted_scopes          TEXT NOT NULL DEFAULT '',
    access_token            BYTEA NOT NULL,
    access_token_expires_at TIMESTAMPTZ,
    refresh_token           BYTEA NOT NULL,
    revocation_reason       TEXT NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at         TIMESTAMPTZ,
    archived_at             TIMESTAMPTZ
);

-- One grant per subject per provider, revoked or not. It is a plain unique index
-- rather than a live-rows-only one because a consent replaces the row rather than
-- sitting beside it: the store upserts onto this key in the caller's transaction,
-- so there is never a second row for the index to have to tell apart. It is also
-- the upsert's conflict target. Two consents racing for one key meet here, and
-- the second waits for the first to commit and then replaces its row, rather
-- than leaving two credentials to one account that both think they are current.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}oauth2_grants_subject_provider_uniq
    ON {{PREFIX}}oauth2_grants (scope, subject, provider);

-- One row per registered passkey: the credential the authenticator minted, the
-- public key a later assertion is verified against, the counter that detects a
-- clone, and which tenant's user it belongs to.
--
-- It is the other half of the WebAuthn story, and the half no library can ship.
-- authentication/webauthnsessions holds a ceremony in flight; primitives-go's
-- authentication/webauthn runs the protocol and hands back a credential with the
-- documented instruction that storing it is the application's job. Without this
-- table every consumer writes it again, and the write that gets dropped is
-- always the same one — see sign_count.
--
-- scope is whose passkey this is: a reseller, a region, a product, or — as the
-- empty string — nobody. Every read of this table filters on it. It has no
-- default, which is the one place this schema departs from the module's habit of
-- defaulting a text column to the empty string: the empty string is a scope,
-- tenancy.Global(), and a column that supplied it for a write which did not name
-- one would hand the global scope to whoever forgot the column. NOT NULL with
-- nothing to fall back on makes that write fail instead.
--
-- belongs_to_user is the consumer's own user id, not the WebAuthn user handle.
-- The handle is what an authenticator returns and what a discoverable login
-- arrives holding; resolving one to a user is the consumer's directory's job and
-- deliberately not this package's — see authentication/passkeys.NewUserSource.
--
-- credential_id is the authenticator's bytes, stored as bytes. It is the value a
-- login arrives holding, so it is what the assertion is looked up by, and base64
-- text in its place would make the lookup depend on which of the two paddings
-- whoever wrote it picked.
--
-- sign_count is the authenticator's counter as of the last assertion this row
-- verified. Its whole purpose is that the next one compares against it: a count
-- that went backwards means two authenticators are answering for one credential,
-- which is a cloned key. A row whose count is never written back is a row that
-- reports nothing forever, so the write that maintains it is one the store
-- refuses to swallow — see authentication/passkeys.Store.RecordUse.
--
-- last_used_at is the instant the ceremony that moved the counter happened, from
-- the caller's clock, and last_updated_at is when the row changed, from the
-- server's. They are two facts rather than two spellings: a deployment answering
-- "when did this passkey last sign anybody in" wants the first, and one auditing
-- its own writes wants the second.
CREATE TABLE IF NOT EXISTS {{PREFIX}}webauthn_credentials (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    belongs_to_user TEXT NOT NULL,
    credential_id   BYTEA NOT NULL,
    public_key      BYTEA NOT NULL,
    transports      TEXT NOT NULL DEFAULT '[]',
    friendly_name   TEXT NOT NULL DEFAULT '',
    sign_count      BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

-- One live row per credential, per scope — and the clause is what makes it
-- correct rather than merely present.
--
-- A plain UNIQUE would cover archived rows too, and a passkey a user revoked
-- could then never be registered again from the same authenticator: the row
-- that refuses the second registration is one nothing can read. No index at all
-- is worse in the other direction — a replayed registration writes a second row
-- for one credential, the two counters advance independently, and clone
-- detection reports on whichever row the lookup happened to reach.
--
-- It is scoped rather than global because a person who belongs to two tenants
-- has one authenticator and may reasonably enroll it in both. The lookup is
-- scoped for the same reason, so a credential registered elsewhere is absent
-- here rather than answerable from the wrong tenant.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}webauthn_credentials_credential_id_uniq
    ON {{PREFIX}}webauthn_credentials (scope, credential_id)
    WHERE archived_at IS NULL;

-- Serves the read that answers "which passkeys does this user have", which is
-- also the read every ceremony assembles its webauthn.User from. The trailing
-- created_at and id are the order that read comes back in.
CREATE INDEX IF NOT EXISTS {{PREFIX}}webauthn_credentials_user_idx
    ON {{PREFIX}}webauthn_credentials (scope, belongs_to_user, created_at, id)
    WHERE archived_at IS NULL;

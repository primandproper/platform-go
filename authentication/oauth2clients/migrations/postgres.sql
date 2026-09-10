-- One table, and it is the whole package: an OAuth2 client somebody registered
-- on purpose, as opposed to one an anonymous caller minted at /register.
--
-- The distinction is why this table exists beside
-- authentication/oauth2serverstore's oauth2_clients rather than inside it.
-- That one is RFC 7591 dynamic registration: written by anonymous callers,
-- bounded by an expiry, and never listed. This one is administered — created
-- through a permissioned RPC, paged, revised, soft-deleted and audited — and
-- none of those is a thing the authorization server's Store models. Two tables,
-- two jobs, and deliberately two names: a deployment runs both, and a schema
-- that named them alike would leave the second CREATE TABLE IF NOT EXISTS a
-- silent no-op followed by a store selecting columns that are not there.
--
-- scope is whose registry this row is in: a reseller, a region, a product, or —
-- as the empty string — nobody, which is the registry the deployment itself
-- keeps. It has no default, and that is deliberate: the empty string is a
-- scope, tenancy.Global(), and a column that supplied it for a write which did
-- not name one would hand the global registry to whoever forgot the column.
-- NOT NULL with nothing to fall back on makes that write fail instead. See the
-- tenancy package.
CREATE TABLE IF NOT EXISTS {{PREFIX}}oauth2_registered_clients (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    -- belongs_to_user is the person who owns this credential, and the empty
    -- string is a registration the deployment or the tenant administers on
    -- behalf of no person.
    --
    -- The two columns together are what let one table serve both arrangements
    -- this package exists to support, without a mode flag. A row at the global
    -- scope naming no owner is infrastructural: an operator minted it, and it
    -- governs how an application speaks to the service for any user. Naming a
    -- scope narrows it to one tenant; naming an owner narrows it to one person.
    -- Every column that is filled in adds a predicate, so there is no setting
    -- that turns a check off — which is the property a boolean would have lost.
    --
    -- It has no default either, and for the sharper version of the scope
    -- column's reason: the empty string is the *more* privileged arrangement,
    -- so a default would file every write that forgot the column as
    -- administered.
    --
    -- It carries no REFERENCES, exactly as password_reset_tokens.belongs_to_user
    -- carries none, so adopting this registry does not mean adopting identity.
    belongs_to_user TEXT NOT NULL,
    -- name is shown on the consent form, and description is for whoever
    -- administers the registry. Both are free text somebody typed — render
    -- them, never trust them.
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    -- client_id is what the client sends at /authorize and /token, and what the
    -- authorization server looks a registration up by. It is a second
    -- identifier rather than the primary key on purpose: the id is what audit
    -- entries, outbox rows and console URLs name, and keeping them apart means
    -- rotating a client_id later does not orphan every reference to the row.
    client_id       TEXT NOT NULL,
    -- secret_hash is the SHA-256 digest of the client secret, hex-encoded,
    -- produced by oauth2server.Hash — the same function the authorization
    -- server compares against, so there is one home for "hex of sha256 of a
    -- client secret" rather than a second copy that can drift. The plaintext is
    -- returned to its creator exactly once and is never stored, so a dump of
    -- this table contains nothing that authenticates.
    --
    -- SHA-256 rather than argon2, for the reason oauth2server's own client
    -- secrets give: a 256-bit secret minted from crypto/rand has no dictionary
    -- to attack, so a work factor buys nothing and costs a verification on
    -- every token request. Passwords are the opposite case and go through
    -- authentication/argon2.
    secret_hash     TEXT NOT NULL,
    -- redirect_uris is the exact set of addresses this client may receive an
    -- authorization code at, matched byte for byte as OAuth 2.1 requires. A
    -- client registers what it will actually send as redirect_uri, ports and
    -- trailing slashes included, because that is the string the comparison is
    -- against.
    --
    -- scopes is what this client may request. The authorization server rejects
    -- a request for anything outside it rather than silently narrowing —
    -- narrowing hands back a token that looks like the one that was asked for
    -- and is not — so the column has to be here for the decorator to answer
    -- with.
    --
    -- Both are JSON arrays in a TEXT column rather than a Postgres TEXT[],
    -- because this schema is rendered for three engines and oauth2server's own
    -- tables already spell a string list that way.
    redirect_uris   TEXT NOT NULL,
    scopes          TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- last_updated_at is when the registration was last revised, and NULL for
    -- one nobody has touched since it was created.
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

-- Globally unique, deliberately not scoped and deliberately not partial.
--
-- Not scoped, because the lookup that reads this column has no scope to pass —
-- it is what resolves one. An identifier taken twice in two registries would
-- make that read ambiguous, so uniqueness is what makes it a total function.
--
-- Not partial, because the lookup has to find an archived registration in order
-- to refuse it, and because re-issuing a retired client_id would re-attribute
-- every token ever minted under it.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_client_id_uniq
    ON {{PREFIX}}oauth2_registered_clients (client_id);

-- Serves the administered page. Leading with scope keeps one registry's page
-- from walking every other registry's rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_scope_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, id)
    WHERE archived_at IS NULL;

-- Serves the self-service page: one person's credentials in one registry. Its
-- own index rather than a prefix of the one above, because that one orders by
-- id immediately after scope and this read walks the cursor across one owner's
-- rows.
CREATE INDEX IF NOT EXISTS {{PREFIX}}oauth2_registered_clients_owner_idx
    ON {{PREFIX}}oauth2_registered_clients (scope, belongs_to_user, id)
    WHERE archived_at IS NULL;

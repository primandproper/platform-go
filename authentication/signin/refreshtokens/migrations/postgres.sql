-- The table a sign-in's refresh tokens live in.
--
-- hash is the digest of the token, never the token. A refresh token is a bearer
-- credential that mints access tokens for as long as a family lives: a database
-- copy — a backup, a replica, a support engineer's query — is a live session for
-- every outstanding row if the column holds the raw value. It is not salted, and
-- does not need to be, because what it digests is thirty-two bytes of randomness
-- rather than something a person chose; there is no dictionary to run against
-- it. It is the primary key rather than a surrogate id, because the only way to
-- name one of these rows is to hold the token it was minted from.
--
-- There are four identifier columns and they answer four different questions.
--
-- scope is whose directory this is — a reseller, a region, a product, or, as the
-- empty string, nobody. It is the scope every sign-in door already takes, and
-- every read of this table filters on it.
--
-- It has no default, deliberately. The empty string is a scope, tenancy.Global(),
-- and a column that supplies it for a write which did not name one hands out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists to
-- make unspellable in Go. NOT NULL with nothing to fall back on makes that write
-- fail instead. See the tenancy package.
--
-- It is denormalized, since a user id belongs to exactly one directory —
-- identity_users is unique on (scope, username) — and it earns the column anyway:
-- this package cannot resolve a subject to a scope without a round trip through
-- the consumer's Directory, and the statements that enumerate must filter without
-- one. That is also why links' argument for omitting a scope column does not
-- transfer here. links revokes across tenants because its subjects are opaque
-- identifiers it cannot resolve; a scoped directory's are not, and a subject id
-- that is already scope-unique loses nothing by being confined to its scope.
--
-- active_account_id is which account inside that directory the access token this
-- row mints is for. Without it an exchange would re-resolve the principal and
-- hand back a token for whatever the user's default account has since become,
-- rather than the account that was proven at sign-in.
--
-- subject_id is which person. It carries no REFERENCES, unlike the equivalent
-- column in the identity schema: this package is usable by an application whose
-- directory is not identity's, and a foreign key would make adopting refresh
-- rotation mean adopting identity too.
--
-- family_id is which continuous login. A family is one sign-in: minted when the
-- password was proven, inherited by every successor the rotation issues, and
-- ended as a unit when a spent token is presented a second time. It is the same
-- concept oauth2_refresh_tokens spells with the same column name, and it is
-- deliberately not called a session — sessions is a different package in this
-- module, with its own store and its own revocation.
--
-- No convention triple. archived_at would keep rows nothing can read while making
-- the sweep the one write unable to reach the rows it exists for, and
-- last_updated_at would be a third copy of redeemed_at and revoked_at, which are
-- the only mutations this row has.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    -- Which door minted this login. It is the one column here that is not an
    -- identifier, and it is not decoration: an administrative sign-in's tokens
    -- are shorter lived than an ordinary one's on purpose, so an exchange that
    -- could not tell the two apart would hand the administrative session an
    -- ordinary token on an ordinary lifetime — the hardening undone at the first
    -- refresh, silently, and in the direction that lengthens it.
    administrative    BOOLEAN NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    -- When the row may be deleted, which is past expires_at by the store's
    -- retention window. It is what the sweep is keyed on, and it is deliberately
    -- not expires_at: a store that collected a row at its own deadline could no
    -- longer tell "already used" from "no such token", which is precisely the
    -- distinction reuse detection is built on.
    purge_after       TIMESTAMPTZ NOT NULL,
    -- NULL until the token is exchanged. The exchange's predicate is
    -- `redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > $now`, which is
    -- what makes one-time use, revocation and expiry a single atomic decision
    -- rather than a read followed by a write another request can interleave with.
    redeemed_at       TIMESTAMPTZ,
    -- NULL until the token is revoked, either as one of its family's or as one of
    -- a subject's.
    revoked_at        TIMESTAMPTZ,
    -- The columns from here on arrived after the table first shipped, as v14's
    -- later schema versions, and sit in the order those versions added them so
    -- that a table created here is the table an upgraded one is.
    --
    -- redeemed_with_key is the idempotency key the exchange that spent this row
    -- presented, and NULL both before the row is spent and when it is spent
    -- without one. It is what lets a client's own retry of an exchange it never
    -- got an answer to be told from somebody else's replay of the same request:
    -- the retry arrives bearing the key that spent the row, and a replay does
    -- not.
    --
    -- It is cleared again by the re-mint that honors it, which is what bounds
    -- the whole mechanism to one re-mint per key. Without that, a single
    -- captured request would mint a fresh live token for as long as the grace
    -- window lasted, where today a spent token is worth nothing to anybody.
    redeemed_with_key TEXT,
    -- successor_hash is the digest of the row this exchange minted, and NULL
    -- until it is spent. It is the second column the retry path needs and the
    -- one that is easy to leave out: honoring a retry means revoking the
    -- successor the first attempt already minted, and "the successor" is not a
    -- row anything else here can name. Revoking the family instead would be the
    -- outcome the retry exists to avoid, and revoking nothing would leave one
    -- login holding two live refresh tokens.
    --
    -- It is a digest for the same reason hash is: a column holding the token
    -- itself would make a backup a live session.
    successor_hash    TEXT,
    -- signed_in_at is when the login a row belongs to began — the issued_at of
    -- the family's first token, copied onto every successor. It is a column
    -- rather than the earliest issued_at a family still has, because that row
    -- is swept at its purge deadline and a login that has refreshed for longer
    -- than a token's lifetime would then report having begun at whichever row
    -- happened to survive. It is what a "where you're signed in" screen says a
    -- login began. It has no default: a mint that forgot it would be stamped
    -- with a login time nobody chose.
    signed_in_at      TIMESTAMPTZ NOT NULL,
    -- access_token_id is the "jti" of the access token minted alongside this
    -- refresh token — by the sign-in that began the login, or by the exchange
    -- that minted this row as a successor.
    --
    -- It is what lets a per-request check tell a family's current access token
    -- from one it has since replaced. A family's live row is its current
    -- refresh token, and the access token minted with it is the one a client
    -- holding the login is meant to be presenting; any other access token
    -- carrying the family's "sid" was minted by a row the family has since
    -- spent, and is superseded. Without the column a check could say whether a
    -- login is still going and nothing about which of its tokens is current.
    --
    -- It is nullable, and NULL is a row whose mint recorded no access token.
    access_token_id   TEXT,
    -- actor_id is the operator behind a login
    -- signin.Service.IssueImpersonationToken began — somebody acting as the
    -- subject — and NULL on every other row. It is what lets a person's list of
    -- where they are signed in say which login is not theirs.
    actor_id          TEXT,
    -- credential_kind is what proved the sign-in that began the login —
    -- signin.CredentialKind: a password, a recovery code, a sign-in link, a
    -- principal the consumer proved, an impersonation, or a kind the consumer
    -- named — carried onto every successor an exchange mints, as signed_in_at
    -- is. It is a fact the service knows at the moment of sign-in rather than
    -- device metadata, which is why it is a column here and a device name is
    -- not: a person's list of where they are signed in can say how each login
    -- happened without the consumer recording it. NULL is a row minted by a
    -- store caller that named none.
    credential_kind   TEXT
);

-- Serves the family revocation a detected token reuse triggers, and the
-- per-request check that reads a family's live row. Without it,
-- revoking a family scans every token this service has ever issued — at the one
-- moment where being slow is being unavailable. Leading with scope keeps one
-- tenant's revocation from walking every other tenant's rows.
--
-- It is a composite rather than a partial index on the unrevoked rows: MySQL has
-- none, and a third spelling of one index across three dialect files is the drift
-- links/database/migrations already declined to carry.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_family_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, family_id);

-- Serves the subject-wide revocation: "disable this account", "sign out
-- everywhere", and the erasure a dataprivacy run performs — and the listing of
-- one person's live logins, which is the same key read rather than written. It cannot be
-- assembled out of family revocations — a caller holding a subject identifier
-- cannot enumerate that person's families, and a loop would leave live whatever
-- was issued while it ran.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_subject_idx
    ON {{PREFIX}}signin_refresh_tokens (scope, subject_id);

-- Serves the sweeper, which is the one statement here that reads rows it cannot
-- name and the one that crosses every scope. Nothing else reads the column.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_refresh_tokens_purge_after_idx
    ON {{PREFIX}}signin_refresh_tokens (purge_after);

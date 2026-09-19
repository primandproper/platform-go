-- The table a sign-in link's tokens live in.
--
-- hash is the digest of the token, never the token. A sign-in link is a bearer
-- credential that mints a session for whoever follows it: a database copy — a
-- backup, a replica, a support engineer's query — is a live sign-in for every
-- outstanding row if the column holds the raw value. It is not salted, and does
-- not need to be, because what it digests is thirty-two bytes of randomness
-- rather than something a person chose; there is no dictionary to run against
-- it. It is the primary key rather than a surrogate id, because the only way to
-- name one of these rows is to hold the token it was minted from.
--
-- Two identifier columns, where the refresh token table beside this one has
-- four, and the two it does without are the difference between the mechanisms.
--
-- scope is whose directory this is — a reseller, a region, a product, or, as the
-- empty string, nobody. It is the scope every sign-in door already takes, and
-- every statement over this table but the sweep filters on it.
--
-- It has no default, deliberately. The empty string is a scope, tenancy.Global(),
-- and a column that supplies it for a write which did not name one hands out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists to
-- make unspellable in Go. NOT NULL with nothing to fall back on makes that write
-- fail instead. See the tenancy package.
--
-- subject_id is which person the link signs in. It carries no REFERENCES, for
-- the reason the refresh token table's equivalent carries none: this package is
-- usable by an application whose directory is not identity's, and a foreign key
-- would make adopting an email-link door mean adopting identity too.
--
-- email_address is not a third identifier: it names no row and resolves nothing.
-- It is the address the mail actually went to, recorded because a link proves
-- control of one inbox and of no other. A redemption that stamped the subject's
-- address proven without it would prove whichever address the row happens to
-- hold at redemption — which is not the one anybody reached — so the service
-- compares the two and refuses a link the subject has since moved away from. It
-- is folded the way the directory folds a handle, so the comparison is an
-- equality rather than a second normalization free to disagree with the first.
--
-- It is stored as it is rather than digested. A digest is worth something
-- against thirty-two bytes from a CSPRNG and nothing against an address, which
-- is drawn from a set somebody can enumerate; digesting it would buy the
-- appearance of protection and the loss of a column an operator can read. The
-- row already carries subject_id, so a reader who can reach this table and the
-- directory could join to the same address anyway.
--
-- There is no family_id, because a link is not a login. It is the thing that
-- begins one: redeeming it mints an access token and a refresh token, and the
-- family those belong to is minted by the service at that moment, exactly as a
-- password sign-in mints one. A family on this row would be a login recorded
-- before anybody had signed in.
--
-- There is no active_account_id either. A password sign-in names the account it
-- wants in its credentials; a link arrives out of an inbox, carrying whatever
-- was in the URL and nothing else, so the account is resolved at redemption from
-- the directory rather than remembered from the request. Storing one would be
-- remembering a choice nobody made.
--
-- There is no administrative column, and its absence is a decision this schema
-- would rather state than imply: there is no administrative sign-in link.
-- authentication/signin's own documentation rules that a service role is the one
-- credential a password alone does not answer for, and a mailed link is strictly
-- weaker than a password. A column here would be the first half of building one.
--
-- No convention triple. archived_at would keep rows nothing can read while making
-- the sweep the one write unable to reach the rows it exists for, and
-- last_updated_at would be a third copy of redeemed_at and revoked_at, which are
-- the only mutations this row has.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_magic_links (
    hash          TEXT PRIMARY KEY,
    scope         TEXT NOT NULL,
    subject_id    TEXT NOT NULL,
    email_address TEXT NOT NULL,
    issued_at     TIMESTAMPTZ NOT NULL,
    expires_at    TIMESTAMPTZ NOT NULL,
    -- When the row may be deleted, which is past expires_at by the store's
    -- retention window. It is what the sweep is keyed on, and it is deliberately
    -- not expires_at: a row collected at its own deadline could no longer tell
    -- "that link was already used" from "no such link", and only one of those is
    -- a sentence a person can act on. The refusal a caller is given collapses
    -- them either way — see internal/queries — but what an operator reads off a
    -- span does not.
    purge_after   TIMESTAMPTZ NOT NULL,
    -- NULL until the link is followed. The redemption's predicate is
    -- `redeemed_at IS NULL AND revoked_at IS NULL AND expires_at > $now`, which is
    -- what makes one-time use, revocation and expiry a single atomic decision
    -- rather than a read followed by a write another request can interleave with.
    redeemed_at   TIMESTAMPTZ,
    -- NULL until the link is revoked. Every outstanding link a person holds is
    -- revoked as a unit — by an account being disabled, or by an erasure. A
    -- completed sign-in is deliberately not among them: see the store's Redeem,
    -- where what burning the rest would cost is argued.
    revoked_at    TIMESTAMPTZ
);

-- Serves the subject-wide revocation, which is the only way these rows are
-- reached other than by holding a token. Leading with scope keeps one tenant's
-- revocation from walking every other tenant's rows.
--
-- It is a composite rather than a partial index on the unrevoked rows: MySQL has
-- none, and a third spelling of one index across three dialect files is the drift
-- links/database/migrations already declined to carry.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_magic_links_subject_idx
    ON {{PREFIX}}signin_magic_links (scope, subject_id);

-- Serves the sweeper, which is the one statement here that reads rows it cannot
-- name and the one that crosses every scope. Nothing else reads the column.
CREATE INDEX IF NOT EXISTS {{PREFIX}}signin_magic_links_purge_after_idx
    ON {{PREFIX}}signin_magic_links (purge_after);

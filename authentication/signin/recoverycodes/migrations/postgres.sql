-- The table a user's recovery codes live in.
--
-- A recovery code is the second factor a person keeps on paper: a set of them is
-- minted at once, shown once, and each one satisfies a second-factor check
-- exactly once, in place of a code from an authenticator they no longer have.
--
-- hash is the digest of the code, never the code. It is not salted, and a fast
-- digest is the right one: what it digests is sixty bits drawn from a CSPRNG
-- rather than something a person chose, so there is no dictionary to run
-- against it, and argon2 buys nothing but a slower sign-in. It is not a key on
-- its own. Sixty bits is enough that nobody guesses a code through a door that
-- counts failures, and not enough that two users' codes can never collide
-- across a whole directory — so the key is the owner and the digest together,
-- and two people holding the same code is two rows that never meet.
--
-- scope is whose directory this is — a reseller, a region, a product, or, as the
-- empty string, nobody. It is the scope every sign-in door already takes, and
-- every statement over this table filters on it.
--
-- It has no default, deliberately. The empty string is a scope, tenancy.Global(),
-- and a column that supplies it for a write which did not name one hands out the
-- global scope to whoever forgot the column — the mistake tenancy.Scope exists to
-- make unspellable in Go. NOT NULL with nothing to fall back on makes that write
-- fail instead. See the tenancy package.
--
-- user_id is whose codes these are. It carries no REFERENCES, for the reason the
-- other two signin tables carry none: this package is usable by an application
-- whose directory is not identity's, and a foreign key would make adopting a
-- recovery path mean adopting identity too. It is also why an erasure has to
-- reach this table on purpose — see the privacy package.
--
-- There is no expiry and no purge_after. A recovery code does not lapse: it is
-- replaced by the person who holds it, or deleted with them, and a sweep would
-- be a deadline nobody chose on the one credential somebody keeps in a drawer
-- for the day they need it.
--
-- No convention triple. archived_at would keep a replaced set readable for no
-- reader — a replaced code must stop working, and a row that could come back is
-- one that might — and last_updated_at would be a second copy of used_at, which
-- is the only mutation this row has.
CREATE TABLE IF NOT EXISTS {{PREFIX}}signin_recovery_codes (
    scope     TEXT NOT NULL,
    user_id   TEXT NOT NULL,
    hash      TEXT NOT NULL,
    issued_at TIMESTAMPTZ NOT NULL,
    -- NULL until the code is spent. The spend's predicate is
    -- `used_at IS NULL`, which is what makes one-time use a single atomic
    -- decision rather than a read followed by a write another request can
    -- interleave with.
    used_at   TIMESTAMPTZ,

    PRIMARY KEY (scope, user_id, hash)
);

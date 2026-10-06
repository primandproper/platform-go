-- One row per phone number a code was texted to: the live code, or the last one
-- a person was sent, for somebody who is not a user.
--
-- The key is (scope, phone_number), and that is the whole of "one live code per
-- number". Issuing again is an upsert onto it rather than an insert beside it, so
-- a person who asks twice and types the second code succeeds, a person who types
-- the first one fails, and there is never a second row for a redemption to have
-- to choose between. id is minted afresh by every issue, so a redemption that
-- read the old row and writes after a new issue has replaced it matches nothing.
--
-- scope is whose directory this is, and like every scope column in the module it
-- has no default: the empty string is tenancy.Global(), and a column that
-- supplied it for a write which did not name one would hand the global scope to
-- whoever forgot the column.
--
-- subject_id is the consumer's own identifier for the person — a contact, a
-- customer, whoever the application took a number down for. It carries
-- no REFERENCES: the person is not a user, and binding this table to identity
-- would make it useless to the application that needs it.
--
-- phone_number is E.164, validated before a write reaches the server. It is
-- stored as it is rather than digested: a digest is worth nothing against a set
-- as enumerable as phone numbers, and the column is what an operator reads.
--
-- code_hash is the digest of the code, never the code, bound together with the
-- row's id so that no two rows share a digest for the same code. It is not what
-- keeps a six-digit code secret from somebody holding a dump — a million
-- candidates is an afternoon for nobody — and the schema does not pretend
-- otherwise. What it buys is that the plaintext is not in a backup, a replica or
-- a support engineer's SELECT, and what bounds a dump reader is the lifetime:
-- minutes, where a dump is read in days.
--
-- attempts and max_attempts are what a link secret never needs and a code does.
-- A six-digit code has a million values; the attempt limit is what stops a
-- guesser working through them. A wrong code counts one, and a code at its
-- limit is dead whatever it is presented with.
CREATE TABLE IF NOT EXISTS {{PREFIX}}phone_codes (
    id           TEXT PRIMARY KEY,
    scope        TEXT NOT NULL,
    subject_id   TEXT NOT NULL,
    phone_number TEXT NOT NULL,
    code_hash    TEXT NOT NULL,
    attempts     BIGINT NOT NULL,
    max_attempts BIGINT NOT NULL,
    issued_at    TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    -- When the row may be deleted, which is past expires_at by the store's
    -- retention window. The sweeper is keyed on it rather than on expires_at so
    -- that "that code was already used" survives the code's own deadline for as
    -- long as an operator might want to read it.
    purge_after  TIMESTAMPTZ NOT NULL,
    -- NULL until the code is spent.
    redeemed_at  TIMESTAMPTZ,
    -- NULL until the code is withdrawn with the rest of its subject's.
    revoked_at   TIMESTAMPTZ
);

-- One row per number per scope, and the issue upsert's conflict target.
CREATE UNIQUE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_phone_uniq
    ON {{PREFIX}}phone_codes (scope, phone_number);

-- Serves the subject-wide revocation, the export and the erasure, which are the
-- only ways these rows are reached other than by the number they were sent to.
CREATE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_subject_idx
    ON {{PREFIX}}phone_codes (scope, subject_id);

-- Serves the sweeper, the one statement here that crosses every scope.
CREATE INDEX IF NOT EXISTS {{PREFIX}}phone_codes_purge_after_idx
    ON {{PREFIX}}phone_codes (purge_after);

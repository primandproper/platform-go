-- Version 2: the two columns that let a client's own retry of an exchange be
-- told from somebody else's replay of it. They shipped in v14.1.0, edited into
-- version 1's CREATE TABLE rather than added beside it, so a table created at
-- v14.0.0 never gained them; this is that change stated as the version it was.
--
-- Both are nullable and neither has a default, because NULL is what each means
-- for a row that predates it: not spent, or spent without a key.
--
-- redeemed_with_key is the idempotency key the exchange that spent this row
-- presented, and NULL both before the row is spent and when it is spent without
-- one. It is what lets a client's own retry of an exchange it never got an
-- answer to be told from somebody else's replay of the same request: the retry
-- arrives bearing the key that spent the row, and a replay does not.
--
-- It is cleared again by the re-mint that honors it, which is what bounds the
-- whole mechanism to one re-mint per key. Without that, a single captured
-- request would mint a fresh live token for as long as the grace window lasted,
-- where today a spent token is worth nothing to anybody.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS redeemed_with_key TEXT;

-- successor_hash is the digest of the row this exchange minted, and NULL until
-- it is spent. It is the second column the retry path needs and the one that is
-- easy to leave out: honoring a retry means revoking the successor the first
-- attempt already minted, and "the successor" is not a row anything else here
-- can name. Revoking the family instead would be the outcome the retry exists to
-- avoid, and revoking nothing would leave one login holding two live refresh
-- tokens.
--
-- It is a digest for the same reason hash is: a column holding the token itself
-- would make a backup a live session.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS successor_hash TEXT;

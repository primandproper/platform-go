-- Version 2: the two idempotency columns. See postgres_v2.sql for what each one
-- is there to tell apart, and for why a table created at v14.0.0 needs this.
--
-- They are VARCHAR although neither is a key or indexed. successor_hash holds
-- what hash holds and is bound the way hash is bound, so a second width for one
-- value would be a width free to disagree; redeemed_with_key is compared in the
-- re-mint claim's predicate, and a bound comparison against an off-row TEXT
-- column buys nothing over an inline one. 255 is also the ceiling the store
-- rejects an over-long key at, so a key MySQL would silently truncate is refused
-- in Go first — see MaximumIdempotencyKeyLength.
--
-- No IF NOT EXISTS: MariaDB accepts it on ADD COLUMN and MySQL does not, and
-- this file is spelled for both. A version runs once, which is what the
-- consumer's migration tool records it for.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN redeemed_with_key VARCHAR(255),
    ADD COLUMN successor_hash    VARCHAR(255);

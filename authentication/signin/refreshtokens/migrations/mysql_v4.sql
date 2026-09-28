-- Version 4: access_token_id. See postgres_v4.sql for what the column answers,
-- and for why NULL is what a row minted before this version carries.
--
-- VARCHAR rather than TEXT because it is compared and projected on the check a
-- consumer may make on every request, and 255 is well past any identifier an
-- issuer mints for a "jti".
--
-- No IF NOT EXISTS: MariaDB accepts it on ADD COLUMN and MySQL does not, and
-- this file is spelled for both. A version runs once, which is what the
-- consumer's migration tool records it for.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN access_token_id VARCHAR(255);

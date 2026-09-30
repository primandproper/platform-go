-- Version 4: access_token_id. See postgres_v4.sql for what the column answers,
-- and for why NULL is what a row minted before this version carries.
--
-- A nullable column with no default is the case SQLite's ADD COLUMN takes
-- without rebuilding the table, which is the reason it is nullable here as
-- everywhere else.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN access_token_id TEXT;

-- actor_id: see postgres_v4.sql. SQLite's ADD COLUMN takes one column at a
-- time, so it is a statement of its own here as everywhere.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN actor_id TEXT;

-- credential_kind: see postgres_v4.sql.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN credential_kind TEXT;

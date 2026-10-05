-- Version 2: the two idempotency columns. See postgres_v2.sql for what each one
-- is there to tell apart, and for why a table created at v14.0.0 needs this.
--
-- SQLite adds one column per statement and has no IF NOT EXISTS for either. A
-- nullable column with no default is the case its ADD COLUMN takes without
-- rebuilding the table.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN redeemed_with_key TEXT;

ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN successor_hash TEXT;

-- Version 2: the index the payment processor's customer read runs on. See
-- postgres_v2.sql for why it is a version of its own and why each column is in
-- it.
--
-- SQLite's spelling is Postgres's: it has partial indexes, and uses one when the
-- read's WHERE carries the index's predicate term for term, which this read's
-- archived_at IS NULL does.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_accounts_customer_idx
    ON {{PREFIX}}identity_accounts (scope, payment_processor_customer_id, id)
    WHERE archived_at IS NULL;

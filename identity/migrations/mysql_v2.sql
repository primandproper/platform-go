-- Version 2: the index the payment processor's customer read runs on. See
-- postgres_v2.sql for why it is a version of its own and why each column is in
-- it.
--
-- MySQL has no partial index, so archived_at is a key column instead, right
-- after the scope — the place version 1's identity_accounts_scope_idx and
-- identity_accounts_billing_idx give it for the same reason. The read matches
-- the scope, archived_at and the customer by equality, so the id it orders by
-- still follows them in key order. The key stays inside InnoDB's limit: scope
-- and the customer are VARCHAR(255) each, the id VARCHAR(64).
--
-- It is an ALTER TABLE rather than an inline key, because the table already
-- exists by the time this runs, and it has no IF NOT EXISTS because MySQL has
-- none for ADD KEY. A version runs once, which is what the consumer's migration
-- tool records it for. So unlike the other two dialects, the whole sequence run
-- a second time over its own tables is not a no-op here: version 1's creates
-- are skipped, and this fails on the key it already added.
ALTER TABLE {{PREFIX}}identity_accounts
    ADD KEY {{PREFIX}}identity_accounts_customer_idx
        (scope, archived_at, payment_processor_customer_id, id);

-- Version 2: the index the payment processor's customer read runs on.
--
-- A processor delivery names the customer and not the account, so every one
-- that arrives is a read of the scope's live accounts keyed on this column —
-- see identity.Store's GetAccountByPaymentProcessorCustomerID. Version 1 shipped
-- nothing that led with it, which left each delivery scanning its scope's
-- accounts; this is that index, added as a version of its own because a
-- database created from version 1 is not going to run its CREATE TABLE IF NOT
-- EXISTS again to find it.
--
-- The columns are the read's: the scope and the customer it matches, then the
-- id it orders by, so the account it answers with is the index's first entry
-- rather than a sort. Partial on live rows, which is the predicate the read
-- carries. It is deliberately not partial on the customer being non-empty as
-- well: the read binds the customer, and a planner holding a generic plan
-- cannot prove a bound value non-empty, so that predicate would cost the read
-- the index it exists for. Not unique either, because nothing makes the column
-- unique; the order by id is what makes two accounts holding one customer
-- answer the same one every time.
CREATE INDEX IF NOT EXISTS {{PREFIX}}identity_accounts_customer_idx
    ON {{PREFIX}}identity_accounts (scope, payment_processor_customer_id, id)
    WHERE archived_at IS NULL;

-- Version 2: who was really acting, when the actor was acting through somebody
-- else's identity. See postgres_v2.sql for why it is a column and why its
-- default is the empty string.
--
-- The column and its index are one statement, because MySQL has no CREATE
-- INDEX IF NOT EXISTS and a version runs once, which is what the consumer's
-- migration tool records it for. It is the actor's width, since it holds the
-- same kind of identifier actor_id does.
ALTER TABLE {{PREFIX}}audit_log_entries
    ADD COLUMN actor_impersonator VARCHAR(255) NOT NULL DEFAULT '',
    ADD KEY {{PREFIX}}audit_log_entries_impersonator_idx (actor_impersonator, recorded_at);

-- Version 2: who was really acting, when the actor was acting through somebody
-- else's identity. See postgres_v2.sql for why it is a column and why its
-- default is the empty string.
--
-- SQLite has no IF NOT EXISTS on ADD COLUMN. A NOT NULL column with a constant
-- default is one its ADD COLUMN takes without rebuilding the table.
ALTER TABLE {{PREFIX}}audit_log_entries
    ADD COLUMN actor_impersonator TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_impersonator_idx
    ON {{PREFIX}}audit_log_entries (actor_impersonator, recorded_at);

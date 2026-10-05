-- Version 2: who was really acting, when the actor was acting through somebody
-- else's identity.
--
-- actor_id stays the subject — the person an impersonated request was filed
-- under, and whose entry it is — and this column names the operator who made
-- it. It is empty for every entry nobody impersonated, which is every entry
-- recorded before this version and almost every one after it, so the default is
-- an absence and not a value: unlike scope, the empty string here means exactly
-- what a write that omitted the column meant.
--
-- It is a column rather than a reserved key in metadata because it is
-- something a reader has to be able to ask for. "What did this operator do while
-- acting as somebody else" is a question the log exists to answer, and a value
-- inside an encoded blob is one no statement can select on in all three
-- dialects.
ALTER TABLE {{PREFIX}}audit_log_entries
    ADD COLUMN IF NOT EXISTS actor_impersonator TEXT NOT NULL DEFAULT '';

-- "What did this operator do as somebody else", as a range scan — the
-- impersonator's counterpart of the actor index.
CREATE INDEX IF NOT EXISTS {{PREFIX}}audit_log_entries_impersonator_idx
    ON {{PREFIX}}audit_log_entries (actor_impersonator, recorded_at);

-- One table, and it is the whole package: a thing somebody told you about your
-- product, and where that report stands.
--
-- The row is conventional — created, soft-deleted, listed and filtered like
-- every other consumer row in this module — with two additions that are the
-- reason this package exists rather than being a shape each consumer writes
-- again. status is where the report stands, and closed_at is when it stopped
-- moving; between them they turn a pile of submissions into a queue somebody
-- can work.
--
-- scope is whose data this row is: a reseller, a region, an account, or — as
-- the empty string — nobody. Every read filters on it. It has no default, and
-- that is deliberate: the empty string is a scope, tenancy.Global(), and a
-- column that supplies it for a write which did not name one hands the global
-- scope to whoever forgot the column. NOT NULL with nothing to fall back on
-- makes that write fail instead. See the tenancy package.
CREATE TABLE IF NOT EXISTS {{PREFIX}}issue_reports (
    id              TEXT PRIMARY KEY,
    scope           TEXT NOT NULL,
    -- reporter is who filed it — a user id in every deployment this module has,
    -- but a string here rather than a reference, because issuereports does not
    -- own the directory and a consumer storing its people somewhere else still
    -- takes bug reports. It is also the column an erasure keys on: the details
    -- below are free text a person wrote, which is personal data whoever they
    -- wrote about.
    reporter        TEXT NOT NULL,
    -- kind is the application's own category — bug, billing, abuse, typo — and
    -- it is what a triage queue groups and routes by. Not validated here: the
    -- catalog of kinds is the consumer's.
    kind            TEXT NOT NULL,
    -- details is what the person actually said. Free text, unbounded, and the
    -- reason this table meets the dataprivacy seam: nothing can promise a
    -- sentence a user typed names nobody.
    details         TEXT NOT NULL,
    -- subject_type and subject_id are what the report is about, as the
    -- application spells it — a table and a row, an entity kind and its id.
    -- Both empty is a report about the product in general, which is the
    -- ordinary case for a bug report filed from a menu.
    --
    -- Two columns rather than one composite key, because a key like
    -- "articles:1234" scopes by construction and cannot be indexed, filtered or
    -- enumerated as the two facts it is: "every report about articles" is a
    -- question this shape answers and that one does not.
    subject_type    TEXT NOT NULL DEFAULT '',
    subject_id      TEXT NOT NULL DEFAULT '',
    -- status is where the report stands. A closed set — see the issuereports
    -- package — held as text rather than as an enum type, because two of the
    -- three dialects have no portable enum and a CHECK constraint would put the
    -- lifecycle in two places that can disagree. What enforces it is the
    -- store's guarded transition, which names the status it believed the row
    -- was in.
    status          TEXT NOT NULL,
    -- resolution is why the report is in the terminal status it is in: the note
    -- a triager leaves when they resolve or decline it. Reopening clears it,
    -- because a reason that no longer holds is worse than none.
    resolution      TEXT NOT NULL DEFAULT '',
    -- closed_at is when the report reached a terminal status, and NULL while it
    -- is still open. A timestamp rather than a boolean beside status, because
    -- "when" answers "whether" and a boolean does not answer "when" — which is
    -- what a time-to-resolution number is entirely about.
    closed_at       TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_updated_at TIMESTAMPTZ,
    archived_at     TIMESTAMPTZ
);

-- Serves the scope's whole list, walked by the cursor's id. Partial on the soft
-- delete, so the index tracks the live reports rather than every report ever
-- filed.
CREATE INDEX IF NOT EXISTS {{PREFIX}}issue_reports_scope_idx
    ON {{PREFIX}}issue_reports (scope, id)
    WHERE archived_at IS NULL;

-- Serves the triage queue: everything open, everything resolved, in one scope.
-- It is the read the status column exists for, and the one a console refreshes
-- on a timer.
CREATE INDEX IF NOT EXISTS {{PREFIX}}issue_reports_status_idx
    ON {{PREFIX}}issue_reports (scope, status, id)
    WHERE archived_at IS NULL;

-- Serves one person's own reports, and the subject access request that collects
-- them. The privacy path reads and deletes by exactly this key.
CREATE INDEX IF NOT EXISTS {{PREFIX}}issue_reports_reporter_idx
    ON {{PREFIX}}issue_reports (scope, reporter, id)
    WHERE archived_at IS NULL;

-- Serves both subject reads. subject_id trails subject_type, so the same index
-- answers "every report about articles" from its prefix and "every report about
-- this article" in full.
CREATE INDEX IF NOT EXISTS {{PREFIX}}issue_reports_subject_idx
    ON {{PREFIX}}issue_reports (scope, subject_type, subject_id, id)
    WHERE archived_at IS NULL;

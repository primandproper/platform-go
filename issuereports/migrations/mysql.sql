-- One table: a thing somebody told you about your product, and where that
-- report stands. The Postgres schema carries the long form of why each column
-- is here; what is written below is what MySQL does differently.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for two
-- MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. details is the
-- exception and stays TEXT — it is what a person typed, it is in no index, and
-- it has no default. The widths are generous rather than measured, and nothing
-- in this package truncates to them, so a consumer who needs more widens the
-- column rather than losing the tail silently.
CREATE TABLE IF NOT EXISTS {{PREFIX}}issue_reports (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    reporter        VARCHAR(64) NOT NULL,
    kind            VARCHAR(255) NOT NULL,
    details         TEXT NOT NULL,
    subject_type    VARCHAR(255) NOT NULL DEFAULT '',
    subject_id      VARCHAR(64) NOT NULL DEFAULT '',
    status          VARCHAR(32) NOT NULL,
    resolution      VARCHAR(2048) NOT NULL DEFAULT '',
    closed_at       DATETIME(6),
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- MySQL has no partial indexes, so unlike the Postgres schema these cover
    -- the whole table and archived_at leads the discriminating columns. Every
    -- read filters on it, so putting it in front keeps these as selective as
    -- the partial clause is elsewhere.
    KEY {{PREFIX}}issue_reports_scope_idx (scope, archived_at, id),
    KEY {{PREFIX}}issue_reports_status_idx (scope, archived_at, status, id),
    KEY {{PREFIX}}issue_reports_reporter_idx (scope, archived_at, reporter, id),
    KEY {{PREFIX}}issue_reports_subject_idx
        (scope, archived_at, subject_type, subject_id, id)
);

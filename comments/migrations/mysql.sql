-- One table: something somebody said, about something the application owns. The
-- Postgres schema carries the long form of why each column is here; what is
-- written below is what MySQL does differently.
--
-- The string columns are VARCHAR where the Postgres schema says TEXT, for two
-- MySQL reasons that apply to different columns: an indexed column needs a
-- bounded key length, and a TEXT column cannot carry a DEFAULT. body is the
-- exception and stays TEXT — it is what a person typed, it is in no index, and
-- it has no default. The widths are generous rather than measured, and nothing
-- in this package truncates to them, so a consumer who needs more widens the
-- column rather than losing the tail silently.
CREATE TABLE IF NOT EXISTS {{PREFIX}}comments (
    id              VARCHAR(64) NOT NULL PRIMARY KEY,
    scope           VARCHAR(255) NOT NULL,
    target_type     VARCHAR(255) NOT NULL,
    target_id       VARCHAR(64) NOT NULL,
    parent_id       VARCHAR(64) NOT NULL DEFAULT '',
    author          VARCHAR(64) NOT NULL,
    body            TEXT NOT NULL,
    created_at      DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    last_updated_at DATETIME(6),
    archived_at     DATETIME(6),

    -- MySQL has no partial indexes, so unlike the Postgres schema these cover
    -- the whole table and archived_at leads the discriminating columns. Every
    -- read filters on it, so putting it in front keeps these as selective as
    -- the partial clause is elsewhere.
    KEY {{PREFIX}}comments_target_idx
        (scope, archived_at, target_type, target_id, parent_id, id),
    KEY {{PREFIX}}comments_target_type_idx (scope, archived_at, target_type, id),
    KEY {{PREFIX}}comments_author_idx (scope, archived_at, author, id)
);

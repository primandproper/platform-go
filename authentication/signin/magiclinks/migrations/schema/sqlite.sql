CREATE TABLE IF NOT EXISTS signin_magic_links (
    hash        TEXT PRIMARY KEY,
    scope       TEXT NOT NULL,
    subject_id  TEXT NOT NULL,
    issued_at   DATETIME NOT NULL,
    expires_at  DATETIME NOT NULL,
    purge_after DATETIME NOT NULL,
    redeemed_at DATETIME,
    revoked_at  DATETIME
);

CREATE INDEX IF NOT EXISTS signin_magic_links_subject_idx
    ON signin_magic_links (scope, subject_id);

CREATE INDEX IF NOT EXISTS signin_magic_links_purge_after_idx
    ON signin_magic_links (purge_after);


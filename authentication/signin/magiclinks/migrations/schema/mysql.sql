CREATE TABLE IF NOT EXISTS signin_magic_links (
    hash          VARCHAR(255) NOT NULL PRIMARY KEY,
    scope         VARCHAR(255) NOT NULL,
    subject_id    VARCHAR(255) NOT NULL,
    email_address VARCHAR(255) NOT NULL,
    issued_at     DATETIME(6)  NOT NULL,
    expires_at    DATETIME(6)  NOT NULL,
    purge_after   DATETIME(6)  NOT NULL,
    redeemed_at   DATETIME(6),
    revoked_at    DATETIME(6),
    KEY signin_magic_links_subject_idx (scope, subject_id),
    KEY signin_magic_links_purge_after_idx (purge_after)
);


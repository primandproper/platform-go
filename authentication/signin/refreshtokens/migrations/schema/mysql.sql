CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              VARCHAR(255) NOT NULL PRIMARY KEY,
    scope             VARCHAR(255) NOT NULL,
    family_id         VARCHAR(64)  NOT NULL,
    subject_id        VARCHAR(255) NOT NULL,
    active_account_id VARCHAR(255) NOT NULL,
    administrative    BOOLEAN      NOT NULL,
    issued_at         DATETIME(6)  NOT NULL,
    signed_in_at      DATETIME(6)  NOT NULL,
    expires_at        DATETIME(6)  NOT NULL,
    purge_after       DATETIME(6)  NOT NULL,
    redeemed_at       DATETIME(6),
    revoked_at        DATETIME(6),
    redeemed_with_key VARCHAR(255),
    successor_hash    VARCHAR(255),
    KEY signin_refresh_tokens_family_idx (scope, family_id),
    KEY signin_refresh_tokens_subject_idx (scope, subject_id),
    KEY signin_refresh_tokens_purge_after_idx (purge_after)
);


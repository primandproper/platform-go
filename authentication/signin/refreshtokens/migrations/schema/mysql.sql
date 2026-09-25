CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              VARCHAR(255) NOT NULL PRIMARY KEY,
    scope             VARCHAR(255) NOT NULL,
    family_id         VARCHAR(64)  NOT NULL,
    subject_id        VARCHAR(255) NOT NULL,
    active_account_id VARCHAR(255) NOT NULL,
    administrative    BOOLEAN      NOT NULL,
    issued_at         DATETIME(6)  NOT NULL,
    expires_at        DATETIME(6)  NOT NULL,
    purge_after       DATETIME(6)  NOT NULL,
    redeemed_at       DATETIME(6),
    revoked_at        DATETIME(6),
    KEY signin_refresh_tokens_family_idx (scope, family_id),
    KEY signin_refresh_tokens_subject_idx (scope, subject_id),
    KEY signin_refresh_tokens_purge_after_idx (purge_after)
);

ALTER TABLE signin_refresh_tokens
    ADD COLUMN redeemed_with_key VARCHAR(255),
    ADD COLUMN successor_hash    VARCHAR(255);

ALTER TABLE signin_refresh_tokens
    ADD COLUMN signed_in_at DATETIME(6);

UPDATE signin_refresh_tokens AS t
  JOIN (
      SELECT scope, family_id, MIN(issued_at) AS began
        FROM signin_refresh_tokens
       GROUP BY scope, family_id
  ) AS f
    ON f.scope = t.scope
   AND f.family_id = t.family_id
   SET t.signed_in_at = f.began
 WHERE t.signed_in_at IS NULL;

ALTER TABLE signin_refresh_tokens
    MODIFY signed_in_at DATETIME(6) NOT NULL;


CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    purge_after       DATETIME NOT NULL,
    redeemed_at       DATETIME,
    revoked_at        DATETIME
);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_family_idx
    ON signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_subject_idx
    ON signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_purge_after_idx
    ON signin_refresh_tokens (purge_after);

ALTER TABLE signin_refresh_tokens
    ADD COLUMN redeemed_with_key TEXT;

ALTER TABLE signin_refresh_tokens
    ADD COLUMN successor_hash TEXT;

ALTER TABLE signin_refresh_tokens
    RENAME TO signin_refresh_tokens_rebuild;

CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         DATETIME NOT NULL,
    signed_in_at      DATETIME NOT NULL,
    expires_at        DATETIME NOT NULL,
    purge_after       DATETIME NOT NULL,
    redeemed_at       DATETIME,
    revoked_at        DATETIME,
    redeemed_with_key TEXT,
    successor_hash    TEXT
);

INSERT INTO signin_refresh_tokens (
    hash, scope, family_id, subject_id, active_account_id, administrative,
    issued_at, signed_in_at, expires_at, purge_after,
    redeemed_at, revoked_at, redeemed_with_key, successor_hash
)
SELECT r.hash, r.scope, r.family_id, r.subject_id, r.active_account_id, r.administrative,
       r.issued_at,
       (SELECT MIN(f.issued_at)
          FROM signin_refresh_tokens_rebuild AS f
         WHERE f.scope = r.scope
           AND f.family_id = r.family_id),
       r.expires_at, r.purge_after,
       r.redeemed_at, r.revoked_at, r.redeemed_with_key, r.successor_hash
  FROM signin_refresh_tokens_rebuild AS r;

DROP TABLE signin_refresh_tokens_rebuild;

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_family_idx
    ON signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_subject_idx
    ON signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_purge_after_idx
    ON signin_refresh_tokens (purge_after);


CREATE TABLE IF NOT EXISTS signin_refresh_tokens (
    hash              TEXT PRIMARY KEY,
    scope             TEXT NOT NULL,
    family_id         TEXT NOT NULL,
    subject_id        TEXT NOT NULL,
    active_account_id TEXT NOT NULL,
    administrative    BOOLEAN NOT NULL,
    issued_at         TIMESTAMPTZ NOT NULL,
    expires_at        TIMESTAMPTZ NOT NULL,
    purge_after       TIMESTAMPTZ NOT NULL,
    redeemed_at       TIMESTAMPTZ,
    revoked_at        TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_family_idx
    ON signin_refresh_tokens (scope, family_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_subject_idx
    ON signin_refresh_tokens (scope, subject_id);

CREATE INDEX IF NOT EXISTS signin_refresh_tokens_purge_after_idx
    ON signin_refresh_tokens (purge_after);

ALTER TABLE signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS redeemed_with_key TEXT;

ALTER TABLE signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS successor_hash TEXT;

ALTER TABLE signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS signed_in_at TIMESTAMPTZ;

UPDATE signin_refresh_tokens AS t
   SET signed_in_at = (
       SELECT MIN(f.issued_at)
         FROM signin_refresh_tokens AS f
        WHERE f.scope = t.scope
          AND f.family_id = t.family_id
   )
 WHERE t.signed_in_at IS NULL;

ALTER TABLE signin_refresh_tokens
    ALTER COLUMN signed_in_at SET NOT NULL;


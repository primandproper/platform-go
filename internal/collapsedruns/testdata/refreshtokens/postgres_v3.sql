-- Version 3: signed_in_at, which is when the login a row belongs to began — the
-- issued_at of the family's first token, copied onto every successor. It is a
-- column rather than the earliest issued_at a family still has, because that
-- row is swept at its purge deadline and a login that has refreshed for longer
-- than a token's lifetime would then report having begun at whichever row
-- happened to survive. It is what a "where you're signed in" screen says a login
-- began.
--
-- It is NOT NULL with no default, and a table that already holds rows cannot be
-- given one in a single statement. So it arrives in three: added nullable,
-- backfilled, and then closed.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS signed_in_at TIMESTAMPTZ;

-- The backfill is a best-effort reading, and the one this column exists to stop
-- anybody needing after today: the family's earliest *surviving* issued_at. A
-- family whose first token has already been swept is stamped with its oldest
-- remaining one, so a login that has refreshed past its first token's purge
-- deadline reads as having begun later than it did. Every row minted from here
-- on carries the real answer. The rows a family still has all agree, which is
-- what the listing reads.
UPDATE {{PREFIX}}signin_refresh_tokens AS t
   SET signed_in_at = (
       SELECT MIN(f.issued_at)
         FROM {{PREFIX}}signin_refresh_tokens AS f
        WHERE f.scope = t.scope
          AND f.family_id = t.family_id
   )
 WHERE t.signed_in_at IS NULL;

ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ALTER COLUMN signed_in_at SET NOT NULL;

-- No index changes. The subject index version 1 created serves the listing of
-- one person's live logins as well as the subject-wide revocation it was made
-- for, since that listing is the same key read rather than written.

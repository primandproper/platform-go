-- Version 3: signed_in_at. See postgres_v3.sql for what the column answers, and
-- for why its backfill is a best-effort reading of the family's earliest
-- surviving issued_at rather than the moment the login began.
--
-- It arrives in three statements, as it does there: added nullable, backfilled,
-- and then closed. The backfill goes through a join on a derived table because
-- MySQL refuses a subquery that reads the table being updated, and the GROUP BY
-- is what keeps the derived table materialized rather than merged back into
-- that refusal. The "MySQL" suite this is checked against is MariaDB, which
-- takes the same spelling.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN signed_in_at DATETIME(6);

UPDATE {{PREFIX}}signin_refresh_tokens AS t
  JOIN (
      SELECT scope, family_id, MIN(issued_at) AS began
        FROM {{PREFIX}}signin_refresh_tokens
       GROUP BY scope, family_id
  ) AS f
    ON f.scope = t.scope
   AND f.family_id = t.family_id
   SET t.signed_in_at = f.began
 WHERE t.signed_in_at IS NULL;

ALTER TABLE {{PREFIX}}signin_refresh_tokens
    MODIFY signed_in_at DATETIME(6) NOT NULL;

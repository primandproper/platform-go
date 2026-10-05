-- Version 4: access_token_id, which is the "jti" of the access token minted
-- alongside this refresh token — by the sign-in that began the login, or by the
-- exchange that minted this row as a successor.
--
-- It is what lets a per-request check tell a family's current access token
-- from one it has since replaced. A family's live row is its current refresh
-- token, and the access token minted with it is the one a client holding the
-- login is meant to be presenting; any other access token carrying the
-- family's "sid" was minted by a row the family has since spent, and is
-- superseded. Without the column a check could say whether a login is still
-- going and nothing about which of its tokens is current.
--
-- It is nullable with no default, and NULL is what it means for a row minted
-- before this version: no access token was recorded. Such a row stops being a
-- family's live one at its next exchange, and the successor that exchange
-- mints carries the column.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS access_token_id TEXT;

-- actor_id is the operator behind a login signin.Service.IssueImpersonationToken
-- began -- somebody acting as the subject -- and NULL on every other row. It is
-- what lets a person's list of where they are signed in say which login is not
-- theirs, and it arrived in this version beside access_token_id because the
-- impersonation's row is what that version's per-request check reads.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS actor_id TEXT;

-- credential_kind is what proved the sign-in that began the login --
-- signin.CredentialKind: a password, a recovery code, a sign-in link, a
-- principal the consumer proved, an impersonation, or a kind the consumer named
-- -- carried onto every successor an exchange mints, as signed_in_at is. It is
-- a fact the service knows at the moment of sign-in rather than device
-- metadata, which is why it is a column here and a device name is not: a
-- person's list of where they are signed in can say how each login happened
-- without the consumer recording it. NULL is a row minted before this version,
-- or by a store caller that named none.
ALTER TABLE {{PREFIX}}signin_refresh_tokens
    ADD COLUMN IF NOT EXISTS credential_kind TEXT;

-- No index changes. The check reads a family's live row by (scope, family_id),
-- which version 1's family index already serves.

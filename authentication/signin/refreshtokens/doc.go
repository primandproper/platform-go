/*
Package refreshtokens is where a sign-in's refresh tokens live: a digest-keyed,
single-use credential grouped into families, one family per login.

It is the SQL implementation of
[github.com/primandproper/platform-go/v14/authentication/signin.RefreshTokenStore],
and it ships the DDL it needs, so adopting rotation is a table and an option
rather than a package somebody writes. The seam is in the parent because the
flow and its storage are genuinely separable; the implementation is here because
a schema, three dialects' worth of statements and a sweeper are not things the
service that orchestrates a password check should be carrying.

# What rotation buys, in one paragraph

An access token is short lived so that a revoked role takes effect soon. A
sign-in is long lived so that nobody types a password every hour. The two are
reconciled by a second credential that mints the first, and the whole risk of
that second credential is that it is worth stealing for as long as the sign-in
lasts. Rotation is the answer: each exchange spends the token and mints its
successor, so a copy somebody took stops working the moment either party uses
theirs — and the moment the loser presents the spent one, the theft is a
detected event rather than a shared session. [SQLStore.Redeem] is where that
happens, and it revokes the whole family in the same transaction it reports the
reuse from.

# A family is one login

family_id groups the tokens one sign-in issued: minted when the password was
proven, inherited by every successor, and revoked as a unit by a detected reuse
or by a sign-out. It is the same mechanism and the same column name
[github.com/primandproper/platform-go/v14/authentication/oauth2serverstore] uses
for the same thing, followed rather than re-spelled.

It is deliberately not called a session.
[github.com/primandproper/platform-go/v14/sessions] is a different package in
this module, with its own store, its own identifier and its own revocation, and
one mechanism answering to one name in two places is exactly the drift a shared
vocabulary exists to prevent. What crosses the wire is still the conventional
"sid" claim — see signin.ClaimFamilyID, where that one translation lives.

# Four identifier columns

They answer four different questions, and the schema is where that is written
out at length — see the migrations package. In short: scope is whose directory,
active_account_id is which account inside it, subject_id is which person, and
family_id is which login.

active_account_id is the one that is easy to leave out and load-bearing. Without
it an exchange re-resolves the principal and hands back a token for whatever the
user's default account has since become, rather than for the account that was
proven at sign-in.

# A retry is not a replay

Rotation's rule — retry with the successor you were given, never with the token
you already sent — is unfollowable by a client that never received an answer.
This store implements
[github.com/primandproper/platform-go/v14/authentication/signin.IdempotentRefreshTokenStore]
so that a retry carrying the idempotency key that spent the token is answered
with a *fresh* successor while the one the lost response carried is revoked,
rather than being treated as the theft it is otherwise indistinguishable from.
Two nullable columns carry it, written by the statements that were already being
written; see [RedeemIdempotently], [RemintGrace] and the migrations package.

# What this store does not do

It reads no user table, so it does not check that a subject exists; the service
re-resolves the principal on every exchange, which is what stops a family
outliving a ban.

It decides no lifetime. How long a refresh token lives is signin's
WithRefreshTokenTTL, resolved before the call and arriving on
signin.RefreshTokenRequest — a store holding a default for it would be a second
place that policy lived. What this store does hold is the retention window past
that deadline, because that is storage's own business: see [WithRetention].

It opens no transaction. Every write here takes the caller's database.Tx, so an
exchange's spend and its successor's mint are one fact — and so are the hooks a
consumer commits alongside a sign-in. The one read, [SQLStore.ListActiveSignIns],
takes the wider executor, so it serves a caller on Client.Reader() and a caller
inside a transaction alike. [Sweep] is the exception and the usual one: a worker
on a timer is the component servicing itself, so it runs on the handle the store
was built with.

# Listing a person's logins

[signin.SignInListingStore] is implemented here: a person's live logins, one
entry per family, and the revocation of one of them keyed on its owner as well
as its family. A login is listed while its family has a row the exchange would
still accept, and each entry says when it began — signed_in_at, carried from the
family's first token onto every successor, because that first row is swept at
its purge deadline and the earliest surviving one would report whenever it
happened to have been minted.
*/
package refreshtokens

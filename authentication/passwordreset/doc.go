/*
Package passwordreset stores the token that lets somebody who cannot sign in
prove they own the address the account was registered with.

This module already ships every other piece of the flow: argon2 hashes the
password that replaces the old one, email sends the link, identity holds the
user the link resolves to. What it shipped nothing for was the middle — the row
recording that a reset was asked for, whom for, until when, and whether it has
already been used — so every consumer wrote it, and identity's own
documentation named this package as the home of a table that did not exist.

# Why it is not four columns on the user

A reset token is a set per user rather than a column on one, it has a lifecycle
of its own, and it is consumed by exactly one flow. Those are the three tests
identity applies to decide what lives on the user row and what lives beside the
engine that reads it, and this fails all three: an application that issues a
second link before the first expires has two live tokens, an expiry and a
redemption are states nothing else on the user has, and no read outside this
flow ever wants them.

# The three things a hand-written one gets wrong

Everything here exists because a reset token store is security-sensitive
boilerplate — code that is short enough to look like it does not need a library
and dangerous enough that the mistakes are vulnerabilities rather than bugs.

The token is stored as a digest and never as itself. What goes in the column is
[github.com/primandproper/primitives-go/cryptography/hashing.Hasher] applied
to the secret, hex-encoded; the secret exists once, in the [Issuance] returned
by [Store.Issue], and this package never has a place to put it again. That is
what makes a database copy — a backup, a read replica, a support engineer's
SELECT — not a password reset for every account with an outstanding link. It is
unsalted, deliberately: what is digested is thirty-two bytes from a CSPRNG
rather than something a person chose, so there is no dictionary to run against
it and a salt would only cost the indexed lookup.

Single use is enforced by the store rather than by the caller. [Store.Consume]
reads the row and stamps its redemption in the transaction it was handed, and it
is the stamp's affected-row count — not the read — that decides who owns the
token. Two requests answering one link at the same instant, in two transactions,
both find the row live; only one of their updates reports a row, and the other is
told the token has already been redeemed. Deciding on the read instead would hand
it to both, and a link that resets a password twice is a link an attacker can
race.

Expiry is refused rather than swept. A row past its deadline is dead to
[Store.Verify] and [Store.Consume] whether or not anything has deleted it yet,
so a deployment that never runs the sweep has a table that grows rather than
links that outlive their TTL.

# Verify and Consume are two calls because they answer two moments

[Store.Verify] is the page load — somebody followed the link and is about to be
shown a form — and it spends nothing, so a user who opens the link, thinks
better of it, and comes back an hour later still has the token they had.
[Store.Consume] is the submit. They return the same [Token] and refuse for the
same three reasons ([ErrTokenNotFound], [ErrTokenExpired], [ErrTokenRedeemed]),
which is deliberate: telling somebody holding an expired link that it expired is
worth more than the nothing an attacker learns from it, since learning it
requires already holding the token.

They run in the caller's transaction, and that is what makes the ordering
question go away. [Store.Consume] takes a
[github.com/primandproper/primitives-go/database.Tx] and [Store.Verify] takes
the wider [github.com/primandproper/primitives-go/database.SQLQueryExecutor],
so the submit verifies, consumes, writes the new password hash through whatever
store owns users, and calls [Store.RevokeForUser] for the links that were
outstanding — all of it in one transaction, committing or unwinding together. A
caller with genuinely nothing to join opens one with
[github.com/primandproper/primitives-go/database.Client.WithTransaction] and
passes the Tx it is handed.

Two transactions is what that removes, and it is the gap worth naming: a
redemption that commits over a password write that then fails costs the user
another email, and a password write that commits over a redemption that then
fails leaves a live reset link for an account whose password has just changed.
The second is a vulnerability rather than a bookkeeping error, and no ordering of
two commits avoids both. One commit does.

# This is not links, and the difference is the table

[github.com/primandproper/platform-go/v14/links] mints single-use, expiring URLs
for four flows and names password reset as one of them. It digests its token,
refuses a replay, and separates Inspect from Redeem exactly as this package
separates Verify from Consume, so the question of which one an application wants
is a fair one and the answer is not "whichever you find first".

links mints whole URLs from a registry of action policies, so one primitive
serves magic login, unsubscribe, and verification without knowing what any of
them means. Its records live behind a
[github.com/primandproper/platform-go/v14/links.Store], and links/database — a
table of its own — is the one implementation. So "which one runs on my
infrastructure" is not the question that separates them: both packages want a
database and nothing else, and links/database buys single use the same way this
package does, from the affected row count of a guarded UPDATE inside one
transaction. It opens that transaction itself, which is the one seam where the
two differ in what a caller can do with them: a redemption here joins the write
it authorizes, and a redemption there commits on its own.

What is left is the table's shape, and what follows from it is tenancy. Every
row here carries a [github.com/primandproper/primitives-go/tenancy.Scope],
which links has no notion of — a scope would have to be something a link is
minted with, and Mint takes none — so a reset that has to be answerable one
tenant at a time is answerable here and nowhere else.

The plural revoke used to be on that list and is not any more.
links.Minter.RevokeForSubject withdraws every link a subject still holds in one
statement, from the same subject column its redemptions are bound by. So the
difference is narrower than it was and it runs the other way.
[Store.RevokeForUser] revokes inside one tenant, keyed on a user this table
stores against; the links form revokes across all of a subject's tenants at
once, keyed on an identifier that table could not resolve if it wanted to.
Which of the two a completed reset wants is a question about how the application
is tenanted, not about which package can be made to answer at all.

So: an application that wants one primitive for its four link flows wants links,
whatever it is running. An application that wants password reset in particular
to be a tenant-scoped, revocable-per-user fact in the same database as its users
— answerable by a report, and revocable one tenant at a time rather than across
all of them — wants this. Nothing stops a deployment using both for different
flows; they share no state and no table.

# Tenancy

Every row carries a
[github.com/primandproper/primitives-go/tenancy.Scope] and every statement
binds it — the module's rule, not an exception to it. A token identifies a
principal in a scope, and there is no unscoped read: an application with one
directory passes
[github.com/primandproper/primitives-go/tenancy.Global] everywhere and
behaves exactly as an unscoped store would.

The store's own machinery is the one exception, and it is narrow. [SQLStore.Sweep]
spans every scope because one scheduler reclaims one table for the whole
deployment, and it deletes by deadline rather than answering a read.

# The table is yours to create

[github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations]
renders the DDL for a dialect and prefix. Nothing here creates a table on its
own: a library that ran DDL against a caller's database would be a library that
decided when a deployment's schema changed.

# The sweeper

Rows expire, but a table does not reclaim them. [WithSweeper] starts a
background delete of everything past its deadline; without it, and without a
scheduler calling [SQLStore.Sweep], the table grows by a row for every password
anybody ever forgot — including the requests nobody followed up, which are the
ones no redemption ever removes.

# Where the SQL comes from

Every statement this package executes is generated. The table's facts — its
name, its columns in projection order, and which of them a write assigns — are
spelled once, in internal/queries. `make generate` renders them through
database/querygen into the canonical .sql files beside that package, in sqlc's
spelling: named statements whose arguments are sqlc.arg references. `make
sqlc_compile` checks every one of them against the DDL migrations produces, on
all three dialects, with no database running; sqlc-gen-unison emits
internal/passwordresetdb from the same files — typed params and methods over
driver placeholders — and that is what the store executes.

So a column renamed in the DDL is a failed generate rather than a scan error at
run time, and the pairing between what a SELECT projects and what a Scan reads
is generated rather than maintained by eye. What this package writes by hand is
which statements it wants; it writes no SQL.

Three of this store's decisions are not consequences of the shapes those
statements are rendered from, and each is argued where it now lives. The
projection excludes token_digest, so nothing hands a caller a stored
credential's digest. Every instant is bound in UTC, which is what makes the
sweep's comparison chronological on SQLite, where a DATETIME column holds text.
And liveness is compared in Go rather than in a predicate: the sweep deletes
rows dead by any reading, but the boundary a user hits at the last second of a
link's life is decided against one clock, in one place, on all three engines.
*/
package passwordreset

//go:generate go run ./internal/queriesgen

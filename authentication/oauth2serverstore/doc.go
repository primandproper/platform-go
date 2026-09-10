/*
Package oauth2serverstore keeps an authorization server's state in SQL tables.

	store, _ := oauth2serverstore.NewStore(&oauth2serverstore.Config{}, db,
		oauth2serverstore.WithSweeper(ctx, oauth2server.DefaultSweepInterval))

	srv, _ := oauth2server.NewServer("https://auth.example", store, authenticator)

This is the implementation a deployment wants. The alternative — four maps
behind an RWMutex — is what a consumer assembling an authorization server from
the reference examples writes, and it works perfectly on one replica.

# What durability actually buys here

Three specific things, and none of them is "data survives a restart" in the
abstract.

An authorization code is written by whichever replica served /authorize and
read by whichever replica serves /token, a few hundred milliseconds later. With
per-process state those are the same replica or the login fails — so a fleet
behind a load balancer fails logins in proportion to how evenly the balancer
spreads them, which presents as a flaky login rather than as a missing
dependency.

A registered client outlives a deploy. Under RFC 7591 dynamic registration
every client is a registered client, so per-process state means a deploy
invalidates the entire client population at once.

A revocation is enforceable. Access tokens here are opaque and checked against
this store on every resource-server request, so a sign-out ends a session now
rather than at the end of the token's lifetime.

# Four tables

	<prefix>oauth2_clients               registrations, with an optional expiry
	<prefix>oauth2_authorization_codes   one row per issued code, spent once
	<prefix>oauth2_access_tokens         opaque tokens, by digest
	<prefix>oauth2_refresh_tokens        rotating tokens, grouped by family

The DDL ships in oauth2serverstore/migrations, rendered for postgres, mysql and
sqlite; hand SQL to database/migrate's WithGeneratedMigration and the tables are
created by the consumer's own migration run.

Every credential table keys on a hex SHA-256 digest, never on the credential. A
dump of this database therefore contains nothing that can be redeemed — a
property the map-backed store gets for free by dying with the process, and the
one that most obviously stops being free the moment the store is a table.

# The two statements that carry the design

Consuming an authorization code is a single guarded UPDATE:

	UPDATE ...codes SET redeemed_at = $now
	 WHERE hash = $hash AND redeemed_at IS NULL AND expires_at > $now

Both predicates are load-bearing. `redeemed_at IS NULL` is what makes two
concurrent redemptions of one code resolve to exactly one winner; `expires_at >
$now` closes the window in which a code expires between a read and the write
that follows it. A store that checked either of those in Go would have both
races, and neither would show up in a test that redeems one code at a time.

Rotating a refresh token is the same statement with `revoked_at IS NULL` added,
which is what keeps a revoked token from reading as a replay — it was never
exchanged, so reporting reuse would revoke a family every time somebody signs
out and their client retries.

A SELECT follows each UPDATE inside the same transaction, but only to explain
what the UPDATE already decided: whether zero rows affected meant absent,
replayed, or expired.

# Nullable timestamps

expires_at on a client, redeemed_at on a code or refresh token, and revoked_at
on either token are all nullable, and the NULL is the point. It distinguishes
"never expires" from "expired at the zero time", and lets every predicate above
be an `IS NULL` rather than a comparison against a magic date.

# Sweeping

Nothing reclaims these rows on its own. WithSweeper runs one, or a scheduler
calls Sweep — which is the better answer for a fleet, since it is one sweep
rather than one per replica. Without either, the authorization code table grows
by one row per login attempt forever, and the client table grows by one row per
anonymous registration.

A revoked token that has not yet expired is deliberately kept: a resource server
holding one is entitled to be told "no" rather than to have its request read as
carrying a token nobody ever issued.

# Conformance

This store and the memory one are held to the same suite,
authentication/oauth2server/oauth2servertest, including the concurrent
redemption and expiry-between-read-and-write cases. That is the only thing that
says a guarded UPDATE and a mutex mean the same thing.

# Where the SQL comes from

Every statement this package executes is generated. The four tables' facts —
their names, their columns in projection order, which of them a write assigns,
and which may be NULL — are spelled once, in internal/queries. `make generate`
renders them through database/querygen into the canonical .sql files beside that
package, in sqlc's spelling: named statements whose arguments are sqlc.arg
references. `make sqlc_compile` checks every one of them against the DDL
migrations produces, on all three dialects, with no database running;
sqlc-gen-unison emits internal/oauth2serverdb from the same files — typed params
and methods over driver placeholders — and that is what the store executes.

So a column renamed in the DDL is a failed generate rather than a scan error at
run time, and the pairing between what a SELECT projects and what a Scan reads
is generated rather than maintained by eye. What this package writes by hand is
which statements it wants; it writes no SQL.

Nineteen statements for what used to be fifteen builders, and the difference is
the point rather than an inflation. A builder took the table as an argument, so
one revocation served the access and refresh tables and one sweep served three —
which is a statement checked against whichever table the argument happened to
name. Enumerated, each is checked against the table it actually runs on.

# Timestamps on SQLite

Every instant this store binds is a UTC time.Time truncated to microseconds.
Postgres and MySQL store those as real temporal types and compare them as such.
SQLite has no date type: a DATETIME column holds text, and a comparison between
two of them compares two strings — so the generated bindings write a bound time
in the shape SQLite's own CURRENT_TIMESTAMP writes, `YYYY-MM-DD HH:MM:SS`, which
is whole seconds.

The consequence is that on SQLite a deadline is stored truncated down to the
second, and so is the instant every guard and every sweep compares it against.
Both ends truncate the same way, so the comparison stays the one the schema
describes; what it costs is that a credential's deadline can land up to a second
earlier than it was issued for. That is invisible at the lifetimes an
authorization server uses — a code lives for minutes and a token for hours — and
it is a reason not to reach for SQLite if some other consumer of these tables
needs sub-second expiry.
*/
package oauth2serverstore

//go:generate go run ./internal/queriesgen

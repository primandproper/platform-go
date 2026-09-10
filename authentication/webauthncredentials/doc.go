/*
Package webauthncredentials stores WebAuthn ceremony state in a SQL table.

A ceremony spans two requests, and in a fleet those two requests land wherever
the load balancer sends them. This is the store that survives that: the row the
first replica wrote is the row the second one reads, and the second one deletes
it as it reads, so a challenge is answerable exactly once.

# Consume is one transaction

The read and the delete are a single transaction, and the delete's affected-row
count — not the read — decides who owns the ceremony. Two replicas answering a
replayed assertion at the same instant both find the row; only one of their
deletes reports a row, and the other is told the session is not there. Doing it
the other way round, or in two statements, leaves a window exactly as wide as
the verification that follows.

# The table is yours to create

migrations renders the DDL for a dialect and prefix. Nothing here creates a
table on its own: a library that ran DDL against a caller's database would be a
library that decided when a deployment's schema changed.

# The sweeper

Rows expire, but a table does not reclaim them. WithSweeper starts a background
delete of everything past its deadline; without it, and without a scheduler
calling Sweep, the table grows by one row per ceremony forever. Expiry itself
does not depend on the sweep — Consume refuses a row past its deadline whether
or not anything has removed it yet.

The deadline the sweep compares against is the server's clock rather than the
injected one, and that is the one thing about this store a clock skew between
the application and its database can reach. It reaches only when a dead row is
reclaimed: Consume decides expiry against the store's clock and does so before
the sweep ever gets to the row, so a ceremony is answerable for exactly as long
as the clock that stamped it says, whatever the database thinks. What the
server's clock buys is a comparison the three dialects spell one way, with no
bound instant whose rendering a driver has to agree about.

# Where the SQL comes from

Every statement this package executes is generated. The table's facts — its
name, its columns in projection order, and which of them a write assigns — are
spelled once, in internal/queries. `make generate` renders them through
database/querygen into the canonical .sql files beside that package, in sqlc's
spelling: named statements whose arguments are sqlc.arg references. `make
sqlc_compile` checks every one of them against the DDL migrations produces, on
all three dialects, with no database running; sqlc-gen-unison emits
internal/webauthndb from the same files — typed params and methods over driver
placeholders — and that is what the store executes.

So a column renamed in the DDL is a failed generate rather than a scan error at
run time, and the pairing between what a SELECT projects and what a Scan reads
is generated rather than maintained by eye. What this package writes by hand is
which statements it wants; it writes no SQL.
*/
package webauthncredentials

//go:generate go run ./internal/queriesgen

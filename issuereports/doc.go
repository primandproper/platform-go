/*
Package issuereports is the durable half of asking your users what is wrong: a
scoped table of what they told you, and where each report stands.

Every product grows this table. A form somewhere collects a category and some
free text, a row is written, and then somebody has to work through them — which
means a status, which means a lifecycle, which means the same four states and the
same guarded move between them written again in each application. What varies is
the catalog of categories and what a report can be about, and both of those are
opaque to this package.

# The lifecycle, and why it is guarded

A report is open, acknowledged, resolved or declined. It is born open; it can be
picked up; it stops in one of the two terminal states; and it reopens to open
rather than to acknowledged, because reopening is what happens when the
resolution turned out not to hold.

[Store.TransitionReport] takes the status the caller believed the report was in
as well as the one it should move to, and the statement requires the row to still
hold it. That is the difference between a queue two people can work and one they
cannot: without the guard, two triagers resolving the same report both succeed,
the second note overwrites the first, and nothing anywhere says so. With it, one
of them gets ErrStatusConflict and re-reads.

The moves the lifecycle admits are checked in Go before the statement runs, so a
nonsensical move is refused rather than merely failing to match; see
[Status.CanTransitionTo].

# The transaction is the caller's

Every write runs inside a transaction the caller owns, and none of them opens
one. Each takes a database.Tx rather than reaching for a writer of the store's
own, and the type is what says so — only database.RunInTransaction produces one,
so the obligation is the compiler's rather than a doc comment's. A consumer with
nothing to join writes:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		return store.CreateReport(ctx, tx, scope, report)
	})

The reason is that a report is rarely the only row a consumer writes. An audit
entry naming who filed or decided it and a data change event on an outbox
somebody fans out are the ordinary companions, and a companion written after the
report's own write has committed is a companion that can go missing while the
report stays. The gap is narrow and it is one-directional — a report with no
event, never an event naming a report that was not written — and nothing outside
this package can close it. A write that could still be called without a
transaction is a write that will be, so there is no such call.

[Store.TransitionReport] is where that matters most, because a decision is an
event before it is a row: a refused audit entry after the move has committed
leaves a status change nobody can attribute. Its guard and the two reads around
it run on the transaction as well, so a report filed and decided in one
transaction resolves rather than reading as absent, and the row it hands back is
the one the entry beside it describes.

The reads take the wider database.SQLQueryExecutor, which is the asymmetry doing
the work: a console paging the triage queue passes Client.Reader() and a caller
that has just filed a report passes the same Tx it wrote through, and sees it.
Neither needs a second method.

# Tenancy

Every read and write takes a tenancy.Scope, and there is no variant of anything
that omits one. A deployment with a single tenant passes tenancy.Global()
everywhere and behaves exactly as it would have without the column.

That includes the two writes that take a whole [Report]: the scope is the
argument's, not the value's. A Report whose Scope names a different tenant is
[ErrScopeMismatch] and one that names none adopts the argument — see [Store] for
why the entity's field is not what the statement binds.

There is deliberately no cross-scope listing — see [Store] for what that costs
and why the alternative is worse.

# The transport

issuereports/grpc serves ten of the eleven store methods over gRPC — the filing,
the reads, the four listings, the revision, the archive and the move — with the
.proto they are described by, a typed client, and the permissions each one wants.
[Store.TransitionReport] is why it is worth having: a compare-and-set is a better
RPC than it is a method call, because over a wire the window between the read a
triager decided from and the write recording their decision is a screen and a
person wide, so the conflict the guard refuses is the ordinary case rather than
the rare one.

What does not cross is [Store.DeleteReportsByReporter], and the reason is the
erasure below: it commits with the rest of a subject's footprint, which is a
property an RPC cannot have. What a request cannot carry is a reporter — a report
is filed by whoever is calling — or a scope, which comes off the principal a
consumer's interceptor resolved.

# Personal data

The details are a sentence somebody typed, and nothing can promise a sentence
somebody typed names nobody. So this table meets the dataprivacy seam like any
other store of personal data, and issuereports/privacy ships the two halves: a
dataprivacy.Collector that returns what a subject filed, and a dataprivacy.Eraser
that destroys it.

The erasure is a hard delete rather than an anonymization, and the reason is that
there is nothing to anonymize down to. Stripping the reporter off a report leaves
the free text, which is the part that identifies people; keeping the text and
losing the reporter would be a worse outcome than either.

# Where the SQL comes from

The store executes no SQL this module has not checked against its own schema.
issuereports/internal/queries describes the table as data; a generator renders
that into one .sql per dialect; sqlc checks each against the DDL
issuereports/migrations ships; and sqlc-gen-unison turns the checked statements
into the typed querier the store calls. A column renamed in a migration is a
failed generate rather than a runtime scan error, on all three dialects at once.

	make generate   # re-renders internal/queries/<dialect>_generated.sql
	make unison     # re-renders the schema and the generated querier

# Getting the table

issuereports/migrations renders the DDL for a dialect and a table prefix. It
ships no numbered migration file, because migration numbers are global per
consumer; hand migrations.SQL to database/migrate's WithGeneratedMigration and
the table is created by your own migration run.
*/
package issuereports

//go:generate go run ./internal/queriesgen

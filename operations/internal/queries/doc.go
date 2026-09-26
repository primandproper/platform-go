/*
Package queries is the operations schema described as data — the canonical table
name and the column list every read projects — together with the statements the
operations store executes.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them into the canonical .sql that sqlc
is run over; the store executes the querier sqlc-gen-unison generates from that
same file. A column list spelled in both places could differ in one name, and
the symptom would be a check that passes over SQL nobody executes.

Why the rendered .sql is committed at all, when the generated Go beside it in
operations/internal/operationsdb carries the same statements in executable form,
is identity's package comment, under "Where the SQL comes from".

# Two statement sets, and two rosters

Render returns one of two corpora. Postgres's is the statements in queries.go:
the create, the claim and the progress flush each hand their row back through
RETURNING, and the guards and the listing's state filter bind their sets as
arrays. MySQL's and SQLite's is the statements in split.go: each of those three
writes is a guarded statement whose row count the store reads first, with the
row read back on the same transaction only when it matched; the sets are closed,
so each member is an argument of its own; and the reap is a candidate read, a
lock by primary key and a delete rather than one statement.

They are two sets rather than one set spelled three ways because the difference
is shape, and unison converges each query onto one Go signature across the
dialects it generates for. So each set is its own roster — unison.yaml
generates Postgres's into operations/internal/operationsdb, and
unison.split.yaml generates the other two into
operations/internal/operationssplitdb — and each gets exactly the checked
guarantee a single roster gets: every statement is checked against the schema
operations/migrations renders for its dialect, with no database running. The
reads are querygen's in both, and the two packages' rows are field for field the
same, which the store's conversions between them assert.

What does not differ between them is any decision. Every guard, every monotonic
floor, the lease horizon, the conditional cancellation and the one clock are the
same in both, and split.go's statements say where each came from.

# What is generated and what is written out

Three reads come from database/querygen: the get by id, the same get with the
scope beside the id, and the batched read the watcher re-reads its subscriptions
through. So does the listing, which is querygen's filtered page with this
schema's three narrowings on it — rendered whole on Postgres, and assembled from
the fragments querygen exports on the other two, where its state narrowing is
the five arguments rather than a set.

Everything else is written out here, and the line is drawn by the SET list rather
than by effort. querygen assigns bound values — a column and the argument it
takes, with last_updated_at stamped by convention — and not one of this
package's writes is that statement. Every one of them assigns an expression: the
revision counter a watcher decides freshness by, the lease horizon a duration is
turned into server-side, the monotonic floor that keeps a straggler flush from
walking a client's progress backwards, the conditional cancellation that resolves
in the same statement that requests it. A generator that could render those would
be a generator with an expression language in it, which is the thing querygen's
closed comparand set exists to refuse.

What the written-out statements do not give up is the guarantee, which is the
whole point of them being here rather than in the store: each is a complete
statement in the committed corpus, checked by sqlc against this package's own
schema, and executed through the generated querier. A renamed column fails
`make unison` for the claim exactly as it does for the get.

# The listing's three narrowings, and why they are three shapes

Kind is an open set — whatever was registered — so it is rendered as an optional
narrowing: a caller who leaves it unset is compared against nothing rather than
against a sentinel. The alternative reading, where an absent kind means the rows
whose kind is the empty string, is a query that runs and answers with a set
nobody asked for.

State is the closed set operations.State enumerates, so it is a bound set
instead, and "every state" is a value rather than an absence: the store binds all
five. On MySQL and SQLite the set is five arguments rather than one, because a
bound set cannot sit in a list query there — see StateFilterArity — and the
closed domain is what makes five enough. That is what keeps the empty set meaning what it means everywhere else in
this module — no keys, no rows — and it is why the narrowing is not eight
statements, one per subset of the three filters, with a store choosing between
eight generated row types.

The scope is the third shape, and it is the plainest: an equality with no way to
leave it off. The optional narrowing's reading of an absent value — compared
against nothing — is every tenant's operations, which is the one answer this
listing must not be able to give. It is the same reason the single read is
rendered twice: an equality on the scope cannot be turned into "any scope" by a
bound value, so the scoped question and the unscoped one are two statements.

# The reads with no scope predicate

The recovery sweep, the retention reap and the read-back at the end of a
cancellation take no scope, and that is the component's own machinery servicing
itself rather than an omission. A sweep that recovered one tenant's operations
would leave every other tenant's stranded, a reap bounded by scope would be a
retention policy that only ran for whoever asked for it, and the cancellation's
read-back holds an id it has itself just written. None is reachable from a
consumer: operations.Store exposes them as the seven methods that take neither
an executor nor a scope, and says so on each.
*/
package queries

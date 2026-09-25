/*
Package queries is the work queue schema described as data — the canonical
table name and the columns its statements touch — together with the statements
the queue executes.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them into the canonical .sql that sqlc
is run over; the queue executes the querier sqlc-gen-unison generates from that
same file. A column list spelled in both places could differ in one name, and
the symptom would be a check that passes over SQL nobody executes.

Why the rendered .sql is committed at all, when the generated Go beside it in
workqueue/internal/workqueuedb carries the same statements in executable form,
is identity's package comment, under "Where the SQL comes from".

# Two statement sets, and two rosters

Render returns one of two corpora. Postgres's is the statements in queries.go:
a claim that selects, locks, leases and hands its rows back in one statement
through RETURNING, and batches bound as one array per column. MySQL's and
SQLite's is the statements in split.go: the same claim as a locking read, a
lease and a read-back by the claim's name, and batches bound as an IN list,
which carries one column — so an enqueue is a statement per row and an outcome
write is a statement per claim.

They are two sets rather than one set spelled three ways because the
difference is shape. unison converges each query onto one Go signature across
the dialects it generates for and refuses one whose shape differs, which is
right: a RETURNING claim on one engine and a three-statement claim on another
under one name would be a method that means two things. So each set is its own
roster — unison.yaml generates Postgres's into workqueue/internal/workqueuedb,
and unison.split.yaml generates the other two into
workqueue/internal/workqueuesplitdb — and each gets exactly the checked
guarantee a single roster gets: every statement is checked against the schema
workqueue/migrations renders for its dialect, with no database running.

What does not differ between them is any decision. The merge rule, the
claimable predicate, the fence on the claim's name, the forward-only
extension, the lock ordering and the one clock are the same in both, and
split.go's statements say where each came from.

# Everything is written out, and the line is not effort

Not one statement here comes from database/querygen, and the reason is the same
one in every case: this table has no id and no listing, and every write assigns
an expression rather than a bound value. querygen assigns a column the argument
it takes, with last_updated_at stamped by convention — a convention this table
does not even carry, since a swept queue has nothing to archive. The claim
increments an attempt counter and derives a lease horizon from a duration; the
enqueue resolves a conflict through a GREATEST, a LEAST and four CASEs over
whether the row it landed on was finished; the release pushes availability
forward by an interval; the reap subtracts a retention window from the server's
own clock. A generator that could render those would be a generator with an
expression language in it, which is the thing querygen's closed comparand set
exists to refuse.

What the written-out statements do not give up is the guarantee, which is the
whole point of them being here rather than in the queue's own package: each is a
complete statement in the committed corpus, checked by sqlc against this
package's own schema, and executed through the generated querier. A renamed
column is a failed `make unison` with no database running.

# A batch is arrays, not tuples

What follows is the Postgres corpus. The split corpus binds a batch the only way
its two engines can, as an IN list that sqlc expands per call — see split.go.

Five of the nine statements act on a batch whose size is decided at the call:
the enqueue and the four keyed writes. A tuple list — or a run of placeholders —
would make the statement's text a function of the batch size, which is the
dynamic SQL this tier exists to replace, so a batch crosses the seam as one
bound array per column instead and the statement is one fixed text however many
items are in it.

Where a batch is one column wide, that is `= ANY(...)` and nothing more, which
is every keyed write: an item is addressed by its key, and the queue-name
predicate that accompanies it is a single bound value. The enqueue is the one
statement carrying several columns per row — a key, a priority and a delay — so
its arrays are unnested WITH ORDINALITY and joined on the position, and the nth
element of each is one entry again. The caller's obligation is the one the
pairing implies: the arrays are parallel, and the queue's own splitting is what
keeps them so.

# One clock, and no exception

The database's clock decides everything about time here, on every engine — at
microseconds on Postgres and MySQL, and at SQLite's millisecond on SQLite,
rounded in the direction split.go's after gives — without the single
exception a timer set has: a work queue names no instants at all. Lease
horizons, availability, completion, the retention window a reap subtracts and
the age the health read reports are all written and compared server-side, and
durations cross the seam as microsecond counts turned into intervals. Nothing in
this corpus binds a timestamp, in either direction, which is why the package it
serves is the one scheduling component in this module with no clock.Clock
option.

That is also why the reap does not come from querygen's bounded prune. Its
horizon would be a ceiling the caller computed, which is the right seam for a
column the application stamped — and completed_at is stamped by the server, by
the statement above it.
*/
package queries

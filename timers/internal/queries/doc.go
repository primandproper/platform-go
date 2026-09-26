/*
Package queries is the timers schema described as data — the canonical table
name and the columns its statements touch — together with the statements the
timer set executes.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them into the canonical .sql that sqlc
is run over; the set executes the querier sqlc-gen-unison generates from that
same file. A column list spelled in both places could differ in one name, and
the symptom would be a check that passes over SQL nobody executes.

Why the rendered .sql is committed at all, when the generated Go beside it in
timers/internal/timersdb carries the same statements in executable form, is
identity's package comment, under "Where the SQL comes from".

# Two statement sets, and two rosters

Render returns one of two corpora. Postgres's is the statements in queries.go:
a claim that selects, locks, leases and hands its rows back in one statement
through RETURNING, and batches bound as one array per column. MySQL's and
SQLite's is the statements in split.go: the same claim as a read of the
candidates, a locking read of those by key, a lease and a read-back by the
claim's name, and batches bound as an IN list, which carries one column — so a
schedule is a statement per timer and an outcome write is a statement per
claim.

They are two sets rather than one set spelled three ways because the
difference is shape. unison converges each query onto one Go signature across
the dialects it generates for and refuses one whose shape differs, which is
right: a RETURNING claim on one engine and a four-statement claim on another
under one name would be a method that means two things. So each set is its own
roster — unison.yaml generates Postgres's into timers/internal/timersdb, and
unison.split.yaml generates the other two into timers/internal/timerssplitdb —
and each gets exactly the checked guarantee a single roster gets: every
statement is checked against the schema timers/migrations renders for its
dialect, with no database running.

What does not differ between them is any decision. The reschedule rule, the due
predicate, the fences, the lock ordering and the one clock are the same in
both, and split.go's statements say where each came from. The one place the
split corpus binds less is the fence: its outcome writes match on the claim's
name and not on the instant, and heldBy says why the name already carries it.

# Everything is written out, and the line is not effort

Not one statement here comes from database/querygen, and the reason is the same
one in every case: this table has no id and no listing, and every write assigns
an expression rather than a bound value. querygen assigns a column the argument
it takes, with last_updated_at stamped by convention. The claim increments an
attempt counter and derives a lease horizon from a duration; the schedule
resolves a conflict through a CASE over whether an instant moved; the release
pushes an instant forward by an interval; the reap subtracts a retention window
from the server's own clock. A generator that could render those would be a
generator with an expression language in it, which is the thing querygen's
closed comparand set exists to refuse.

What the written-out statements do not give up is the guarantee, which is the
whole point of them being here rather than in the set's own package: each is a
complete statement in the committed corpus, checked by sqlc against this
package's own schema, and executed through the generated querier. A renamed
column is a failed `make unison` with no database running.

# A batch is arrays, not tuples

What follows is the Postgres corpus. The split corpus binds a batch the only way
its two engines can, as an IN list that sqlc expands per call — see split.go.

Four of the eight statements act on a batch whose size is decided at the call:
the schedule, the two keyed writes, and the cancel. A tuple list would make the
statement's text a function of the batch size, which is the dynamic SQL this
tier exists to replace — so a batch crosses the seam as one bound array per
column instead, and the statement is one fixed text however many timers are in
it.

Where a batch is one column wide, that is `= ANY(...)` and nothing more. Where it
is several — a schedule's key, instant and payload, or a firing's key and the
instant that fences it — the arrays are unnested WITH ORDINALITY and joined on
the position, so the nth element of each is one row again. The caller's
obligation is the one the pairing implies: the arrays are parallel, and the
store's own splitting is what keeps them so.

# The fence, and why a firing is not just a key

A firing is addressed by its key and the exact instant the claimant was handed.
That is what makes the reschedule race harmless: a retirement or a hand-back
carrying a stale run_at matches nothing, so a timer moved while it was being
fired keeps its new schedule instead of being marked against the old one. It is
the same "matches nothing" outcome a lapsed lease already produces, so it needs
no handling anywhere else.

# One clock

The database's now() decides everything about time except which instant a timer
was scheduled for. The lease horizon, the due comparison, the lateness a claim
reports, the retention window a reap subtracts — all of it is written and
compared server-side, and durations cross the seam as microsecond counts turned
into intervals. run_at is the single exception and is bound absolutely, because
it is the thing the caller actually meant; whether it has arrived is still the
server's answer. The split corpus binds it as a count of microseconds since the
epoch rather than as a time, because a bound time reaches SQLite as whole-second
text, and turns the count into the engine's stored instant server-side, rounded
up — see split.go's instant.

That is also why the reap does not come from querygen's bounded prune. Its
horizon would be a ceiling the caller computed, which is the right seam for a
column the application stamped — and fired_at is stamped by the server. Under
the injected clock this package takes, a caller-computed horizon and a
server-stamped column are not merely skewed but arbitrarily far apart.
*/
package queries

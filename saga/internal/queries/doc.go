/*
Package queries is the saga schema described as data — the canonical table name
and its columns in the order every read projects them — and the statements the
store runs over it, rendered for each of the three dialects it serves.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them into the canonical .sql files sqlc
is run over; the store reads the same table name and column list to build the
querier and to name what it binds. A column list spelled in both places could
differ in one name, and the symptom would be a check that passes over SQL nobody
executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are [Render]'s output — see saga/internal/queriesgen — and what the store
executes is the querier sqlc-gen-unison generates from those, in
saga/internal/sagadb.

# What querygen renders and what is written out

Two of the statements come from [querygen]: the get, and the create.
Everything else is written out in this package, and the line is not effort — it
is what the statement does that querygen has no way to say.

  - Several of them assign something other than a bound value. querygen
    assigns a column and the argument it takes; the claim increments an attempt
    counter server-side, the two advances and the requeue zero one, the requeue
    clears the resume hint to the empty sentinel, and some of them drop a
    lease to NULL outright rather than binding one. Rendering those would need
    an expression language in querygen, which is exactly what its closed set of
    comparands exists to refuse.

  - Several of them guard on a *set*: the two statuses a worker can advance, or
    the statuses an operator is resuming an instance out of. A querygen.Match is an
    equality on one column, and two of them are an AND rather than an IN.

  - The claim's candidate read orders across three columns rather than by the id
    a keyset walk pages over, compares two columns against one instant, admits a
    NULL as "nobody holds this", and takes the row lock that makes two workers
    polling at once mean anything.

  - The two listings narrow by a column querygen's list has no predicate for —
    see below.

What they do not give up is the guarantee, which is the whole point of them
living here rather than in the store: each is a complete named statement in the
committed corpus, checked by `sqlc compile` against the DDL saga/migrations
renders, on every dialect, and executed through the generated querier. A renamed
column is a failed `make unison` for the claim exactly as it is for the get.

Where a dialect fact is involved, the fragment is querygen's rather than a copy
of it: the filter window, the archived toggle, the keyset predicate, the two
counts, the page-size clause, and the bound-set predicate all come from that
package's exported fragments. What is written out here is the shape, not the
dialect.

# Why a listing binds five statuses

A listing narrowed by a set would have to bind one, and a bound set is the one
predicate that cannot sit in a paged read on all three dialects: SQLite numbers
the bare markers a sqlc.slice expansion produces one past the highest it has
seen, so the cursor and the page size bound after it collide with the set's own
elements — silently, on the two arguments that decide which rows come back.

The status domain is closed at five, so it does not need one. The predicate
names five arguments and the store decides which five: a caller asking for the
stuck ones binds 'stuck' in every slot, and a caller asking for nothing in
particular binds all five, which is the same rows the predicate's absence would
have returned. See [StatusFilterArity], which saga's own tests hold to the size
of the domain.

The definition filter is the other half of that decision and goes the other way:
it is enumerated as a second named statement rather than made optional, because
an optional equality is not a predicate a caller can relax — a listing asked for
nothing in particular must not come back with the rows whose definition is the
empty string.

# The timestamps this schema binds

created_at is supplied by the create rather than defaulted by the database,
which is a departure from this module's convention and the one place this schema
takes one. A saga instance is a schedule as much as a record: next_attempt, the
lease horizon, and every backoff this package computes come off the clock the
Runner and the Worker share, and created_at is the tie-break the claim orders by
right after next_attempt. Two clocks in one ordering is a claim index ordered
against something nothing measured, and a test that advances a fake clock cannot
say when an instance was started. last_updated_at is bound for the same reason,
in every transition, rather than stamped CURRENT_TIMESTAMP.

On SQLite those bound times are stored to the second. The generated bindings
format a time argument into the shape SQLite's own CURRENT_TIMESTAMP writes,
because that column compares as text and two shapes that merely sort alike is
the bug that costs a whole timezone offset; the price is sub-second precision,
on that dialect only.

Every comparison this schema makes with one of them is therefore satisfied at
most a second *early*, never late — a lease looks lapsed before it truly is, and
a retry becomes due before its backoff has fully elapsed. That direction is the
safe one for both: the lease is a hint that stops two workers doing the same
work at once, and the thing that actually stops them is the distributed lock the
Worker holds per step and the status guard every advance carries. What it does
mean is that a Step.Delay finer than a second does not survive a restart on
SQLite — the instance is claimable at the next poll rather than after the delay
— which is a scheduling resolution rather than a correctness loss, and it is
SQLite's alone.
*/
package queries

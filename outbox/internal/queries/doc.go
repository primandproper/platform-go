/*
Package queries is the outbox schema described as data — the canonical table
name and its columns in the order every statement names them — together with the
statements the Writer and the Relay execute, rendered from it.

It exists because those facts had two consumers that must not disagree. The
generator behind `make generate` renders this table into the canonical .sql
files sqlc is run over; the Writer and the Relay executed their own copy of the
same column lists to build their inserts, their projections and their scan
targets. A column list spelled in both places could differ in one name, and the
symptom would be a check that passes over SQL nobody executes. So it is spelled
once, here, and both halves read what the generator emitted rather than a second
copy of it.

The .sql files beside this one are the output — see [Render] and
outbox/internal/queriesgen. What the outbox executes is the querier
sqlc-gen-unison generates from them, in outbox/internal/outboxdb.

# Rendered and authored

A few come from database/querygen: the failure write, which assigns bound
values to a row addressed by its id, and the reap, which is that package's
bounded prune. The rest are written out here in full.

The line between them is not effort, and it is not "querygen was not finished".
querygen renders statements that assign *bound values* and address rows by
equality, which is the shape a row-oriented store is nearly all of. The rest
are outside it, and each is outside it for a reason that would still be true if
querygen grew:

  - Two read the outbox table through itself. The claim's ordering predicate is
    a correlated NOT EXISTS over a self-join, which is a shape querygen does not
    render and should not learn to — and it is two statements rather than one
    because the lock clause is text, not a value. See selectClaimable.
  - Two assign something a caller cannot bind. `attempts = attempts + 1` is a
    value the server computes from the row, and the two columns a
    mark-published clears are NULLs the statement owns rather than arguments —
    there is nothing a caller could pass that should leave a published message
    holding a lease.
  - One is an aggregate, which a corpus of row statements has no shape for.
  - One binds a creation instant. querygen owns created_at on purpose — a
    caller-supplied one is how a row's creation time comes to disagree with its
    id — and this column is not a row-creation time at all: it is the instant
    the emitting transaction chose, which the claim's order, the per-key
    predicate, the backlog's age and the first next_attempt all read.

And one shape is absent rather than authored. The batched read querygen renders
orders by the column it keyed on; the fetch here owes the publish order, which
is the tuple the claim calls "earlier", so it is written out for its ORDER BY
and assembles its projection from the column list above regardless.

Authored does not mean unchecked. Each is a complete named statement in the same
committed corpus, analyzed by the same pinned sqlc against the same schema on
all three dialects, and executed through the same generated querier. What it
gives up is having been derived from a column list.

# One row per statement

There is no multi-row insert here, and an Enqueue of four messages executes four
INSERTs inside the caller's transaction. A VALUES list whose length is the
batch's makes the statement's text a function of its argument count, which is
the dynamic SQL this corpus exists to retire; the dialects that could bind a
batch as arrays instead are not all three of these. The round trips are inside a
transaction the caller has already opened, which is where they cost least, and
the atomicity the outbox exists for is the transaction's rather than the
statement's.

# The tenancy exception, and why it is the whole file

Nothing here binds a scope, and outbox_messages has no column for one. That is
the cross-tenant exception this module documents rather than an omission: one
relay drains one outbox for a whole deployment, and every statement below either
addresses rows that relay is already holding or the queue as a whole. A message
belongs to whatever wrote it, inside the transaction that wrote it, and the
payload the Relay republishes is opaque to this package — so there is no
consumer read here to scope, and a scope column would be a filter no statement
could honestly apply.

# A note on SQLite and the second

SQLite has no temporal type, and the generated bindings write a bound time as
the text CURRENT_TIMESTAMP produces — YYYY-MM-DD HH:MM:SS, UTC, which is what
makes the comparisons here lexicographic and correct. What it also is, is
second-granular: a bound instant is floored on the way in, and so is every
timestamp the server stamps.

Three comparisons here are against a bound instant, and flooring moves all three
in the same direction — earlier — so each is early rather than late, and early is
the safe end of every one:

  - A message's next attempt, and the claim's "now": a retry becomes due up to a
    second before its backoff said. It is a retry rather than a deadline, and
    the poll interval is the same order as the error.
  - A lease horizon: a claim expires up to a second early, so a message can be
    reclaimed a second sooner than the relay holding it expects. Publication is
    at-least-once by contract and the lease is tens of seconds, so what this can
    cost is a duplicate at the boundary — which is the thing a consumer of this
    outbox is already required to tolerate.
  - The reap's retention horizon: a published message is removed up to a second
    before the window ends, against a window measured in hours.

The fourth use of an instant is not a comparison against a bound one. created_at
is compared against another stored created_at, in the claim's per-key predicate
and its ORDER BY, so flooring moves both sides together: two messages enqueued
within one second come back tied on the column and are separated by the id,
which is exactly what the tuple is for and what an Enqueue of several messages
already relied on.

A statement added here that compares a bound instant the other way — one that
must not fire early — is one this note does not cover.
*/
package queries

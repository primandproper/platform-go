/*
Package queries is the webhooks schema described as data — the canonical table
names and each table's columns in the order every read projects them — together
with the thirty statements the store executes, rendered from it.

It exists because those facts had two consumers that must not disagree. The
generator behind `make generate` renders these tables into the canonical .sql
files sqlc is run over; the store executed its own copy of the same column
lists to build its projections and its scan targets. A column list spelled in
both places could differ in one name, and the symptom would be a check that
passes over SQL nobody executes. So it is spelled once, here, and the store
reads what the generator emitted rather than a second copy of it.

The .sql files beside this one are the output — see [Render] and
webhooks/internal/queriesgen. What the store executes is the querier
sqlc-gen-unison generates from them, in webhooks/internal/webhooksdb.

# Rendered and authored

Most of the statements come from database/querygen: the upserts, the
keyed reads, the three paged lists in both directions, the archives, the plain
insert, and the three writes that move a dispatch between states. The rest
are written out here in full.

The line between them is not effort, and it is not "querygen was not finished".
querygen renders statements that assign *bound values* and address rows by
equality, which is the shape a row-oriented store is nearly all of. The rest
are the ones outside it, and each is outside it for a reason that would still
be true if querygen grew:

  - Five read a table through itself or through more than one other. The claim's
    ordering predicate is a correlated NOT EXISTS over a self-join; the claimed
    read is a three-table join projecting a whole second entity beside the
    first; the bounded reaps delete through a subquery over the table being
    deleted from, in the two spellings the dialects have between them.
  - One assigns an expression. `attempts = attempts + 1` is a value the server
    computes from the row, not one a caller binds, and rendering it would mean
    an expression language in querygen — which is exactly what the closed
    [querygen.Comparand] set exists to refuse.
  - One is an aggregate, which a corpus of row statements has no shape for.
  - Two reach a scope that lives on another table, through a join and through a
    subquery. querygen's single-row statements are one table's shape plus
    predicates, and these are rows whose owner is somebody else's column.
  - Two bind a creation instant. querygen owns created_at on purpose — a
    caller-supplied one is how a row's creation time comes to disagree with its
    id — and these two columns are not row-creation times at all: a dispatch's
    is the instant the emitting transaction chose, which its claim order and
    the backlog's age both read, and an attempt's is when the request was
    issued rather than when the log line landed.

Authored does not mean unchecked. Each is a complete named statement in the
same committed corpus, analyzed by the same pinned sqlc against the same schema
on all three dialects, and executed through the same generated querier. What it
gives up is having been derived from a column list — so each one that projects
or joins is still assembled from the [Table] values above rather than from a
column list spelled a second time.

# The scope, in every statement that owes one

Two of the five tables carry a scope. An endpoint's is whose subscriber it is;
a delivery's is whose event it was. The other three borrow one: a subscription's
is its endpoint's, an attempt's is its delivery's, and a dispatch has none
because the worker that reads it serves every tenant at once.

So the roster is checkable, and TestRender_ScopesEveryConsumerRead is where it
is checked rather than asserted. Every statement that answers a consumer read of
an endpoint, a subscription, a delivery or an attempt names the scope — on the
row where the row carries it, through a join or a subquery where it does not.

Everything else is enumerated there with its reason, and there are three kinds.
Four statements are the second half of an operation that was scoped one
statement earlier, inside the same transaction, on an endpoint id that came out
of that scoped read rather than off the wire: the subscription write, its
read-back, the endpoint's own subscription list, and the reconciling archive.
One reads the scope rather than filtering on it, which is the collision check in
front of the endpoint upsert and the whole question it asks. The rest — the
claim, the lease, the claimed read, the two outcomes, the health probe, the
three reaps, and the two writes the fan-out makes — are the delivery worker's
own machinery, which is the exception webhooks' package comment documents: one
worker drains one queue for a whole deployment.

# A note on SQLite and the second

SQLite has no temporal type, and the generated bindings write a bound time as
the text CURRENT_TIMESTAMP produces — YYYY-MM-DD HH:MM:SS, UTC, which is what
makes the comparisons here lexicographic and correct. What it also is, is
second-granular: a bound instant is floored on the way in, and so is every
timestamp the server stamps.

Four comparisons here are against a bound instant, and flooring moves all four
in the same direction — earlier — so each of them is early rather than late, and
early is the safe end of every one:

  - A dispatch's next attempt, and the claim's "now": a retry becomes due up to
    a second before its backoff said. It is a retry rather than a deadline, and
    the poll interval is longer than the error.
  - A lease horizon: a claim expires up to a second early, so a dispatch can be
    reclaimed a second sooner than the worker holding it expects. Delivery is
    at-least-once by contract and the lease is minutes long, so what this can
    cost is a duplicate at the boundary — which is the thing a subscriber is
    already required to tolerate.
  - The reap's retention horizon: a delivered dispatch is removed up to a second
    before the window ends.

None of them is a correctness boundary a second wide, and none of them is late.
A statement added here that compares a bound instant the other way — one that
must not fire early — is one this note does not cover.

# What a paged list is

Three of these are paged, and each is two statements: the ascending page and
the descending one, under [querygen.DescendingName] of it. A direction is which
way the ORDER BY runs and which way the cursor comparison points, which is
statement text on all three engines rather than a bound value, so the store
picks between two generated methods rather than assembling an ORDER BY. Each
carries the two counts filtering.QueryFilteredResult reports, so a page and its
totals are one round trip and one snapshot.
*/
package queries

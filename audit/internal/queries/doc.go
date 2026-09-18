/*
Package queries is the audit schema described as data — the two canonical table
names and their columns in the order every read projects them — and the
statements run over it, rendered for each of the three dialects audit serves.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them into the canonical .sql files sqlc
is run over; the recorder, the reader, the prune target and the eraser read the
same table names and column lists to build the querier and to name what they
bind. A column list spelled in both places could differ in one name, and the
symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are [Render]'s output — see audit/internal/queriesgen — and what the stores
execute is the querier sqlc-gen-unison generates from those, in
audit/internal/auditdb.

# Four callers, one corpus

Three of them are audit's own — the recorder that appends, the reader that pages
and verifies, the prune target a retention.Sweeper drives — and the fourth is
dataprivacy/auditerasure, which owns no table at all. Its two deletes and its
count address these tables, so they are here: a second corpus over somebody
else's schema would be a second place a column rename has to be noticed, and the
eraser binds the querier it is handed to the erasure transaction it is given.

# What querygen renders and what is written out

Ten of the twenty statements come from [querygen]: the chain's read, its
insert-ignore and its two guarded updates, the entry insert and the two
single-entry reads, and the bounded prune. Everything else is written out here,
and the line is not effort — it is what the statement does that querygen has no
way to say.

  - The paged list carries a window over recorded_at. querygen derives the
    created_after/created_before pair from the presence of created_at, and this
    table has none: recorded_at is the caller's fact and the hash covers it, so
    a database-assigned creation stamp would make every entry read back as
    tampered. The window is therefore named rather than derived, through
    [querygen.Generator.WindowConditions] — which is the fragment rather than a
    second copy of it, because the sentinel an absent bound coalesces to is
    three spellings of one interval.

  - The verification walk is that window again over one scope, ordered by
    position and unbounded. A page size on it would report a chain intact as far
    as the statement happened to look.

  - The scope listing is a DISTINCT, and its keyset compares against a column
    whose empty value is a real scope — so it is two named statements, the first
    page and the pages after it, where every other keyset walk in this module
    coalesces an absent cursor to the empty string that no id holds.

  - The backlog count is bounded by a subquery, so that the cost of the reading
    does not grow with the size of the problem it reports.

  - The prune bounds are two aggregates, one of them carrying its own predicate
    through a CASE. That is an expression rather than a comparison, which is
    exactly what querygen's closed set of comparands exists to refuse.

  - The prune target orders descending, and [querygen.Read.Order] sorts one way.

  - The chain's locked read is the unlocked one with a clause on it, and a
    clause is statement text on every server here.

  - The eraser's three key on a bound set and on a disjunction, neither of which
    a [querygen.Match] can say.

What they do not give up is the guarantee, which is the whole point of them
living here rather than in a store: each is a complete named statement in the
committed corpus, checked by `sqlc compile` against the DDL audit/migrations
renders, on every dialect, and executed through the generated querier. A renamed
column is a failed `make unison` for the prune bounds exactly as it is for the
get.

Where a dialect fact is involved, the fragment is querygen's rather than a copy
of it: the window's sentinel, the two counts, the keyset predicate, the
page-size clause, the bound-set predicate, and every predicate a
[querygen.Match] renders all come from that package's exported fragments. What
is written out here is the shape, not the dialect.

# Why the list narrows six ways and enumerates nothing

audit.Query is six independent selectors. Enumerated as statements they are
sixty-four, and a hundred and twenty-eight once each is emitted in both
directions; expressed as a bound set they are a predicate that cannot sit in a
paged read on two of the three dialects. So each is a
[querygen.OptionalNarrowing], which an absent argument leaves alone.

The reading matters more here than it usually does, and the scope is why. The
empty string is the chain platform-level events are recorded in, so an absent
scope read as "the empty one" would answer an operator console asking for
everything with the platform's own events — and the narrowing reading is the
only one that leaves "only the platform's" expressible at all.

The single-entry get carries that same scope narrowing, and nothing else. It is
the one statement here where the two predicates over the scope column sit side
by side: the get compares against the optional argument, and the chain reads
beside it compare against the column's own bound value, because those address a
chain and this one confines a read. A get keyed on the id alone was the read
that answered across every tenant with nothing on it saying so, which is what
audit.Reader.Get's *tenancy.Scope now makes a caller decide.

# The timestamps this schema binds

recorded_at is the caller's, always. It is folded into every entry's digest and
re-hashed on verification, so a value that changed on the way through would read
as tampering on every entry in the table; audit truncates it to microseconds at
the write site for the same reason, since Postgres and MySQL store no more than
that.

The chain row's last_updated_at is the opposite case and is stamped by the
server, which is what [ChainStateColumns] carrying the column arranges. Nothing
compares it and nothing hashes it, so the argument for binding an application
clock does not reach it, and the argument against two clocks writing one column
does.

On SQLite the bound times are stored to the second, because the generated
bindings format a time argument into the shape SQLite's own CURRENT_TIMESTAMP
writes — that column compares as text, and two shapes that merely sort alike is
the bug that costs a whole timezone offset. Every comparison this schema makes
against one is therefore satisfied at most a second early: a retention horizon
reaches a row a second before it strictly should, against a window measured in
years. What it must not do is affect the digest, and it does not — the hash is
taken over the value in Go before the write, and audit reads recorded_at back
through the same formatting on the way out.
*/
package queries

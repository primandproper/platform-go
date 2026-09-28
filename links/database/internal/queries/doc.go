/*
Package queries is the action link schema described as data: the table's name,
its columns in the order every statement lists them, the subset a resolution may
assign, and the two that may be NULL.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store executes the querier
sqlc-gen-unison generates from those same files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

So it is spelled once, here. The .sql files beside this one are the generator's
output — see [Render] and links/database/internal/queriesgen.

# A handful of statements, and none of them standard

querygen.Generator.StandardCRUD emits the set a conventional table gets, and
this table gets none of it. Its id is the digest of a credential rather than a
surrogate, so there is no cursor to page and nothing to list — the only way to
name a link is to hold the token it was minted from. It carries no convention
triple: an archived_at would keep rows nothing can read while making the sweep
the one write unable to reach the rows it exists for, and a last_updated_at
would be a second copy of resolved_at, since resolution is the row's only
mutation.

What is left is a handful of statements, each named for what it does to a link
rather than for the shape it came from.

  - InsertLink writes one mint. It is a plain INSERT, so a digest collision is a
    failed mint rather than a silently replaced row — and a replaced row would
    be one caller handed a URL that redeems another caller's link.
  - GetLink is the read Inspect makes and the one a resolution begins with,
    keyed on the id alone.
  - ResolveLink spends or withdraws a link, guarded on the resolution not yet
    having happened. Its row count is what decides who owns the link when two
    requests answer one token at once, and it is the reason this store needs no
    lock service at all.
  - SweepLinks removes everything past its purge deadline, against a horizon the
    store binds from the minter's own clock.

Three properties of the current statements are decisions rather than
consequences of the shapes they are rendered from, and [Render]'s comment argues
each: the guard on resolved_at rather than on state, the UTC binding SQLite's
lexical comparison depends on, and the liveness comparison links.Record.Usable
makes in Go rather than in a predicate here.

# What this package does not describe

The scan side. The generated querier's row types carry it, derived from the same
column lists, which is the point: a column renamed in a migration is a failed
`make unison` rather than a runtime scan error on whichever dialect noticed
first.
*/
package queries

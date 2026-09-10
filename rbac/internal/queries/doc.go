/*
Package queries is the authorization policy schema described as data: the four
canonical table names, the column list the two named tables share, and the
statements the resolver runs over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the resolver executes the querier
sqlc-gen-unison emits from those same files. A column spelled in both places
could differ in one name, and the symptom would be a check that passes over SQL
nobody executes.

What this package replaced is thirteen fmt.Sprintf calls over format strings,
with the table prefix interpolated and the bind markers numbered by hand through
dialect.Placeholder. Nothing checked any of it against the schema until a
container test ran it, and hand-numbered placeholders have a characteristic
distribution of failure: correct on Postgres, where the marker carries its own
number, and silently wrong on MySQL and SQLite, where it does not.

# The two shapes this schema is

Roles and permissions are the same table twice. Each is a name an operator gave
something with prose beside it, each carries the convention triple, and each is
written by the same upsert converging on the same unique index — so they share
one column list, [NamedColumns], and get one statement apiece rather than two
that could come to differ.

The mapping tables are the other shape and carry none of the triple, by design
and by assertion: internal/schemaconvention exempts them as mapping rows
rewritten wholesale with their role. Nothing lists, filters or soft-deletes an
edge on its own — revoking one deletes the row, and archiving either endpoint
already hides every edge that names it — so what they get is the id-less child
set: an insert, a delete keyed on the parent, and a read through a join.

# The resolution

One statement, and the only recursive one in this module. It is rendered from
querygen.Generator.ClosureQuery, which is the shape this port is what added, and
the reason it is a shape rather than a statement written out here is that its
two properties are the whole of its correctness and neither is visible in a diff
of the SQL:

  - UNION rather than UNION ALL, which is what makes it terminate on a hierarchy
    that contains a cycle. Seed and UpsertRole reject cycles before they can be
    written, but a table an operator edited by hand has no such guard, and a
    query that hangs on the path deciding whether a request is allowed is a far
    worse failure than one that returns a slightly surprising union.

  - The archived predicate at every join rather than only at the seed. That is
    what makes archiving a permission revoke it everywhere on the next
    resolution without touching a mapping row, and what stops an archived
    intermediate role from going on granting what it inherited.

Rendering it rather than writing it out is what makes a corpus that has one and
not the other unrepresentable rather than merely unusual.

# Where a predicate is deliberately absent

Three reads here see archived rows, and each does so by being rendered from a
column list without archived_at — which is how every statement in this module
says it wants a derived predicate left off. The name is what makes them: a name
stays reserved once used, because freeing an archived role's name would re-grant
whatever a new role of that name holds to everyone still carrying the old
assignment. So a lookup that skipped archived rows would report the name free
and hand the write to a unique index.

The column is projected instead of compared, which is what lets the caller tell
"no such role" from "one that is archived" — different next moves, since only
the second has an id to write back under.
*/
package queries

/*
Package queries is the comments schema described as data: the canonical table
name, the table's columns in the order every read projects them, and the subsets
each write assigns.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders this table through database/querygen
into the canonical .sql files sqlc is run over; the store reads the same table
name to render its prefixed identifiers. A column list spelled in both places
could differ in one name, and the symptom would be a check that passes over SQL
nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and comments/internal/queriesgen.

# Why the table takes no standard set

[querygen.Generator.StandardCRUD] emits the set a conventional table gets: reads
and writes keyed on the row's own id and, where the caller names one, on an
ownership column.

Every statement here is keyed on the scope, reads included, because a comment is
somebody's data and there is no read that may omit whose. That much StandardCRUD's
ownership column would have covered. What it does not cover is the rest: three
keyed lists, two keyed deletes, and a projection whose id predicate has to come
off for the two writes that address a set of rows rather than one.

# The empty parent, and the statement it saves

parent_id holds the empty string for a comment that replies to nothing. That is a
storage decision with a query consequence, and the consequence is why it was
made: "the roots of this target" is `parent_id = ”` and "this root's replies" is
`parent_id = $root`, which is one statement with a different bound value. Under
NULL the first would have been `IS NULL` — statement text, not an argument — and
the discussion's two reads would have been two statements, two plans, and two
places for one projection to drift.

# What the column lists decide

querygen derives a statement's predicates from the column list it is handed,
which is why [Table.ColumnsExcept] exists: a statement that keys on something
other than the row's own id says so by handing over a list without the id, and
what it projects is a separate list. Two statements here use it — the target
sweep, which keys on a thing, and the erasure, which keys on a person.

The archived predicate comes from the same list, and one read here needs its
complement instead. GetArchivedComment is the row the archive hands back, and the
row an archive just moved is the one row every other statement over this table is
written not to return — so it is rendered from no column list at all and carries
archived_at IS NOT NULL as a match of its own. That makes the read-back an
assertion rather than a second lookup: a guard that touched nothing cannot be
read back as a live row.

# The three lists

There are three paged lists — one level of a discussion, one target type, one
author — and each is rendered in both directions, because a direction is which
way the ORDER BY runs and which way the cursor comparison points, and those are
statement text rather than a bound value on all three engines.

They are three statements rather than one carrying optional predicates.
[querygen.OptionalNarrowing] exists and would have collapsed them into one, at
the cost of a statement whose predicates a server discovers per execution and
whose plan therefore cannot be the one the schema's indexes were written for.
What three statements cost is text, and nobody writes this text by hand.

# The counts that are not statements

A comment count beside a target — "12 comments" — is a number every consumer
wants, and this corpus has no COUNT. It does not need one: every list querygen
renders carries filtered_count and total_count as subqueries on its rows, so the
page and the number describing it come back from one statement and one snapshot
of the table. A second round trip would count a table that had moved on since the
page was read.
*/
package queries

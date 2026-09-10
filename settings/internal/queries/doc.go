/*
Package queries is the settings schema described as data: the canonical table
names, each table's columns in the order every read projects them, and the two
subsets a write may assign.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders these tables through
[querygen.Generator.StandardCRUD] and the keyed forms beside it into the
canonical .sql files sqlc is run over; the store reads the same table names to
render its prefix. A column list spelled in both places could differ in one name,
and the symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and settings/internal/queriesgen.

# Which queries each table gets

[Table.Options] is where a table says what it is beyond a list of columns, and
three of the four options carry a fact that a column list cannot:

  - Ownership is the scope column, so every emitted statement is keyed on it.
    It is named rather than inferred, because a table whose rows are readable
    across scopes and one whose rows are not look identical from the columns.
  - Nullable names the columns a write may set to NULL, which lives in the
    schema neither this package nor querygen reads. There is one here, and it
    carries the whole of the distinction between a setting with no default and a
    setting whose default is the empty string.
  - Updatable names the columns the standard update assigns, and everything
    else assignable becomes immutable to it.

Definitions is the only table that gets the standard set, and every one of its
assignable columns is updatable — which is unusual in this module and is a
statement about where the guard lives rather than an absence of one. A
definition's kind and its enumeration decide how every value already stored
against it is read, so the write that changes either is guarded by a walk of
those values rather than by the column being frozen. Freezing them would be the
same refusal with no way to say yes.

Values gets none of the standard set. Its columns are conventional and not one
of its statements is: every single-row statement keys on the (scope,
subject_type, subject_id, definition_id) quadruple rather than on the id the
table also carries, which the standard set has no way to express, and its create
is an upsert because that quadruple is unique across live and archived rows
alike — so setting a value that was once cleared revives the row rather than
adding a second one, and the creation time it keeps is when the subject first
answered.

The options table is not a [Table] at all, for the reason identity's role tables
are not: no id, no scope, no convention triple, and no standard query of any
kind. What it has is a parent, a value, and the two writes that rewrite an
enumeration wholesale — plus the batched read that hydrates a page of
definitions with theirs, which is the read a page would otherwise make one query
at a time.

# The keyed variants

A table's standard queries are not all of what a store runs against it, and the
difference used to be the hand-written half everywhere in this module. Each of
these is rendered here, through querygen's keyed forms, which are the standard
statements with more predicates rather than a second rendering of them:

  - the definition read keyed on the name application code spells, which is the
    read every value-side method begins with
  - the name collision check, keyed on the name and excluding the row being
    updated — rendered from no column list at all, because the unique index
    covers archived rows and so must the read
  - the read-back of created_at, which the create does not carry because the
    database owns the column
  - the value read keyed on its natural key, and the two paged value lists —
    one per subject, one per definition
  - the batched option read, keyed on a whole set of definition ids at once
  - the read-back ClearValue answers with, the value it took back. It is
    rendered from no column list either, and for the opposite half of the same
    reason: the archived predicate querygen would derive is exactly the one that
    excludes the row it is called to describe, so it carries the complement
    instead and sees only rows a clearing moved. ArchiveDefinition has no
    companion here, because the definition it retires is still the row its id
    names and the live read still reaches it beforehand.

What none of the batched forms can express is the empty batch, which is why the
store answers that before it calls: see [querygen.Generator.SetReadQuery].
*/
package queries

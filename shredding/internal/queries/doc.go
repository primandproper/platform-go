/*
Package queries is the subject-key table described as data: its canonical name,
its columns in the order every read projects them, and the natural key every
statement addresses a row by.

It exists because those facts have two consumers that must not disagree. The
generator behind `make unison` renders them through database/querygen into the
canonical .sql files sqlc is run over; the generated querier beside them is what
the store executes. A column list spelled in both places could differ in one
name, and the symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here. The .sql files beside this one are the generator's
output — see [Render] and shredding/internal/queriesgen.

# The natural key

This table has no id. Its primary key is (subject_type, subject_id), and that is
not a stylistic choice about surrogate keys: it is the constraint that enforces
one live key per subject, and one live key per subject is the difference between
a shred that works and one that leaves half the ciphertext readable. A user and
an account that happen to share an identifier are two subjects with two keys.

That shape is what makes this package the pattern the module's other natural-key
tables follow, and it is expressible with nothing querygen did not already have.
A single-row statement's id predicate is rendered from the column list it is
handed — so a list with no id renders none — and the key goes in [querygen.Match]
values instead, which is what Match has always been for. The conflict target of
the insert names the same two columns, because Postgres matches ON CONFLICT
against a unique index the table actually has and this table's is the primary
key.

# Which columns each statement is handed

[Columns] is the whole row and [RecordColumns] is the five columns every
statement here works in. The gap between them is two columns and it is
deliberate:

  - archived_at is in the schema for the convention's sake and no statement
    writes it, so no statement filters on it either. A key row is a record of
    destruction, and hiding one would hide the evidence the row exists to be —
    so the shape lists handed to querygen leave the column out, which is how a
    statement says it renders no archived predicate.
  - last_updated_at is assigned by the shred, and by the shred alone, from the
    same bound instant as shredded_at. querygen stamps the column from the
    server's clock for any statement whose shape list names it, so the shape
    list leaves it out and the SET list names it instead — see [Render].

RecordColumns is one list rather than a projection and an insert list that
happen to match, because the row *is* the record: the two columns it omits are
the two nothing supplies at insert time and nothing reads back.

# The three statements

There are three, and there used to be four builders, because the mint and the
tombstone rendered the same text. Both write the whole row and skip one that is
already there; what differs is only what they bind — key material and no
destruction time for the mint, a destruction time and no key material for the
tombstone. Two names over one statement would have been the drift this package
exists to prevent, so there is one:

	GetSubjectKey     the single-subject read, keyed on the pair
	InsertSubjectKey  the mint and the tombstone, insert-ignore on the pair
	ShredSubjectKey   the destruction, guarded on shredded_at IS NULL

The insert is an insert-ignore rather than an upsert, and it is annotated
:execrows, because the count is the answer. The loser of a mint race has
generated a key it must throw away; a mint aimed at a subject who has already
been shredded must not revive them. Both are "the row that is there wins,
unchanged", which is what [querygen.Generator.InsertIgnoreQuery] renders and
what an upsert with an empty conflict branch is refused for not being.

The shred's guard is the same kind of mechanism. shredded_at IS NULL is what
makes the destruction idempotent without a read first: a second call matches
nothing, and zero rows affected is how the caller learns the destruction was
somebody else's and reads back whose timestamp to report.
*/
package queries

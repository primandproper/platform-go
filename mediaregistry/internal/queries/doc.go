/*
Package queries is the upload registry's schema described as data: the canonical
table name, its columns in the order every read projects them, and the subsets a
write may assign.

It exists because those facts have two consumers that must not disagree. The
generator behind `go generate` renders the table through
[querygen.Generator.StandardCRUD] and the keyed forms beside it into the
canonical .sql files sqlc is run over; the store reads the same table name to
render its prefix. A column list spelled in both places could differ in one name,
and the symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and
mediaregistry/internal/queriesgen.

# Which queries the table gets

The standard set, less two. There is no existence check, because every caller
that would ask has to read the row anyway: whether a caller may see an object is
decided from the row's owner and scope, so "is it there" and "may you have it"
are one read.

There is no update either, and that is the shape of the table rather than an
omission. Every column is a fact about bytes that are already in a bucket — the
key they live at, their type, their length, who put them there, what they hang
off — and none of those changes without the object itself changing, which is a
new object and a new row. A table whose standard update assigned every mutable
column would let a caller point one row's key at another row's bytes, which is
the one edit that makes the registry lie about the bucket.

What is emitted beyond the standard set is three keyed reads and two keyed
lists:

  - the owner page and the belongs-to page, each a standard list with one more
    predicate — [querygen.Generator.ListQueries] renders both directions, since
    a paged list is two statements
  - the read keyed on the object key, which is what a request holding a URL path
    rather than a row id runs
  - the collision check behind ErrObjectKeyTaken, which reads the id alone and
    is rendered from no column list at all, because the unique index covers
    archived rows and so must the read
  - the read the archive answers with, rendered from no column list for the same
    reason and carrying the complement of the predicate the others carry —
    archived_at IS NOT NULL — because the row an archive just hid is the one row
    every other read here is written not to see

# The belongs-to pair is one key

belongs_to_type and belongs_to_id are always bound together, in the schema's
index and in the statement's predicate. An id without its type is an id that
means something else in another one of the consumer's tables, so a list keyed on
the id alone would return one caller's receipts beside another's avatars.
*/
package queries

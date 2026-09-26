/*
Package queries is the third-party grant schema described as data: the canonical
table name, its columns in the order every read projects them, and the nine
statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files.

# The nine statements

  - PutGrant writes a consent as an upsert onto the subject's key, so a new
    consent replaces whatever grant the key held, live or revoked, and two
    racing consents converge on the later one rather than colliding.
  - GetGrant reads one live row by id; the writes read back through it.
  - GetGrantForProvider is the read a caller holding a subject makes.
  - GetRevokedGrant is the revocation's read-back, keyed on the complement of
    the liveness predicate every other single-row read carries.
  - ListGrantsForSubjects is every grant a batch of subjects hold, revoked ones
    included and neither token projected — the export's read.
  - RefreshGrant is the compare-and-set a refresh writes through.
  - RevokeGrant and ArchiveGrant are the two halves of a revocation: who, with
    the tokens emptied, and then when.
  - DeleteGrantsForSubject is the erasure.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/grants/internal/queriesgen. They exist so `sqlc compile` can
check these statements against the schema migrations renders, at build time, with
no database running, and so the drift gate can pin the committed text byte for
byte against the renderer.
*/
package queries

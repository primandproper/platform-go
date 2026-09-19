/*
Package queries is the registered-passkey schema described as data: the canonical
table name, its columns in the order every read projects them, and the seven
statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

# Why there is no standard set

[querygen.Generator.StandardCRUD] brings a paged list with a filter window, a
cursor and two counts, and nothing here pages. A person has a handful of
passkeys, and the read every ceremony makes wants all of them at once — see
[listForUser], which states the ruling. What is left of the standard set after
the list is a get, a create and an archive, and this file needs each of the three
in a shape the standard one does not have: the create's column list drops
last_used_at, and the archive is keyed on the owner as well as on the row.

# The seven statements

  - CreateCredential writes one registered passkey. It is a plain insert: a
    collision on the live-rows unique index means the credential is already
    enrolled, and there is no converging write that is right for that.
  - GetCredential reads one live row by id, and is the read-back the create and
    the sign-count write answer with. It is not on the Store.
  - GetCredentialByCredentialID is the read a login runs, keyed on the bytes the
    authenticator returned.
  - GetArchivedCredential is the archive's read-back, keyed on the complement of
    the predicate every other single-row statement here carries.
  - ListCredentialsForUser is every live passkey one user has, unpaged.
  - RecordCredentialUse writes the sign count back, which is the statement the
    table exists for.
  - ArchiveCredentialForUser revokes one, with the owner in the predicate rather
    than in a check the caller makes first.

# Where RETURNING would have gone

Three of the seven are a write followed by a read of the row it moved, and on
Postgres and SQLite two of those pairs could have been one statement. MySQL has
no RETURNING, so the read-back is a second statement on the same transaction
everywhere — one shape on three dialects rather than two shapes and a reason.
The archive's read-back is the one that needs a statement of its own, because
every other single-row read here filters archived_at IS NULL and would find
nothing; see [archivedRead].

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/passkeys/internal/queriesgen. Nothing imports them and nothing
executes them: they exist so `sqlc compile` can check these statements against the
schema migrations renders, at build time, with no database running, and so the
drift gate can pin the committed text byte for byte against the renderer.
*/
package queries

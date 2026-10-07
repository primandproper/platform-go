/*
Package queries is the sign-in device schema described as data: the canonical
table name, its columns in the order every read projects them, the subsets a
write assigns, and the five statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

# The five statements

  - UpsertSignInDevice records where a login was renewed from: a new row for a
    login's first token, and the same row brought up to date by every refresh.
  - ListSignInDevicesForFamilies is the annotator's read: one person's rows,
    among the families one listing returned.
  - ListSignInDevicesForUser is every row one person has, for an export.
  - DeleteSignInDevicesForUser removes them, for an erasure.
  - SweepSignInDevices removes every row whose login can no longer be alive.
    It is the one statement that spans every scope.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/signin/devices/internal/queriesgen. Nothing imports them and
nothing executes them: they exist so `sqlc compile` can check these statements
against the schema migrations renders, at build time, with no database running,
and so the drift gate can pin the committed text byte for byte against the
renderer.
*/
package queries

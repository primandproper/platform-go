/*
Package queries is the recovery code schema described as data: the canonical
table name, its columns in the order every read projects them, the subsets a
write assigns, and the six statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

# The six statements

  - InsertRecoveryCode writes one code of a set. It is a plain INSERT, so a
    digest repeated inside a set is a failed replacement rather than a person
    holding one fewer code than they were shown.
  - RecoveryCodeUnspent is the check a sign-in makes before its transaction
    opens. It decides nothing; the spend repeats every test it makes.
  - SpendRecoveryCode burns one code, guarded on its not having been spent. Its
    row count is what decides who owns the code when two sign-ins present one at
    once.
  - CountUnspentRecoveryCodes is how many a person has left.
  - ListRecoveryCodesForUser is every code a person holds, less the digest, for
    an export.
  - DeleteRecoveryCodesForUser removes a person's whole set: the first half of a
    replacement, and the whole of an erasure.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/signin/recoverycodes/internal/queriesgen. Nothing imports them
and nothing executes them: they exist so `sqlc compile` can check these statements
against the schema migrations renders, at build time, with no database running,
and so the drift gate can pin the committed text byte for byte against the
renderer.
*/
package queries

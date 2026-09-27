/*
Package queries is the texted-code schema described as data: the canonical
table name, its columns in the order every read projects them, and the eight
statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files.

# The eight statements

  - IssuePhoneCode writes a code as an upsert onto (scope, phone_number), so a
    new code replaces whatever the number held and two racing issues converge
    on the later one.
  - GetPhoneCode reads the row one number holds, in any state, without the
    digest.
  - SpendPhoneCode redeems a code, guarded on every row-state test its answer
    rests on, the digest and the attempt count included.
  - CountPhoneCodeAttempt counts a wrong code, as a compare-and-set of the count
    the redemption read.
  - RevokePhoneCodesForSubject withdraws every unspent code one person holds.
  - ListPhoneCodesForSubject and DeletePhoneCodesForSubject are the export and
    the erasure.
  - SweepPhoneCodes removes everything past its purge deadline.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/phonecodes/internal/queriesgen. They exist so `sqlc compile`
can check these statements against the schema migrations renders, at build time,
with no database running, and so the drift gate can pin the committed text byte
for byte against the renderer.
*/
package queries

/*
Package queries is the sign-in link schema described as data: the canonical
table name, its columns in the order every read projects them, the subsets a
write assigns, and the five statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

# The five statements

  - InsertMagicLink writes one mint. It is a plain INSERT, so a digest collision
    is a failed write rather than a silently replaced row, and minting again
    leaves whatever is outstanding alone.
  - GetMagicLink is the read the redemption makes on both of its paths, keyed on
    the hash and the scope, projecting everything but the hash. On the winning
    path it is how the subject is learned, since MySQL has no RETURNING; on the
    losing path it is what a span records about which guard refused.
  - RedeemMagicLink spends a link, guarded on the redemption not yet having
    happened, on the revocation not having happened, and on the deadline not
    having passed. Its row count is what decides who owns the link when two
    requests present one at once.
  - RevokeMagicLinksForSubject withdraws every outstanding link one person holds.
    It is not assembled out of per-link revocations, and [Render] says why it
    cannot be.
  - SweepMagicLinks removes everything past its purge deadline, against a horizon
    the store binds from its own clock.

Three properties of the current statements are decisions rather than consequences
of the shapes they are rendered from, and [Render]'s comment argues each: the
projection that excludes the hash, the UTC binding SQLite's lexical comparison
depends on, and the deadline guard this corpus puts in the redemption's predicate
where two of its four siblings make the same comparison in Go.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/signin/magiclinks/internal/queriesgen. Nothing imports them
and nothing executes them: they exist so `sqlc compile` can check these statements
against the schema migrations renders, at build time, with no database running,
and so the drift gate can pin the committed text byte for byte against the
renderer.
*/
package queries

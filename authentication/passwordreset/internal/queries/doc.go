/*
Package queries is the password reset token schema described as data: the
canonical table name, its columns in the order every read projects them, the
subsets a write assigns, and the seven statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through
the querier sqlc-gen-unison generates from those files. A column list spelled in
both places could differ in one name, and the symptom would be a check that
passes over SQL nobody executes.

# The seven statements

  - InsertToken writes one issuance. It is a plain INSERT, so a digest
    collision is a failed write rather than a silently replaced row.
  - GetTokenByDigest is the lookup Verify and Consume both begin with, keyed on
    the digest and the scope, projecting everything but the digest.
  - RedeemToken spends a token, guarded on the redemption not yet having
    happened. Its row count is what decides who owns the token when two requests
    answer one link at once.
  - RevokeTokensForUser destroys one principal's outstanding tokens, sparing
    the redeemed ones.
  - DeleteTokensForUser destroys every token one principal holds, sparing
    nothing. It is the revocation without its third predicate, and the
    difference is a revocation against an erasure — see [Render].
  - ListTokensForUser reads every token one principal holds, oldest first,
    projecting everything but the digest. It is the one list here and it is
    unpaged, which is why this corpus still has no cursor and no filter window.
  - SweepExpiredTokens removes everything past its deadline, against a horizon
    the store binds from its own clock.

Three properties of the current statements are decisions rather than
consequences of the shapes they are rendered from, and [Render]'s comment argues
each: the projection that excludes token_digest, the UTC binding SQLite's
lexical comparison depends on, and the liveness comparison the store makes in Go
rather than in a predicate here.

The rendered .sql files beside this one are the generator's output — see
[Render] and authentication/passwordreset/internal/queriesgen. Nothing imports
them and nothing executes them: they exist so `sqlc compile` can check these
statements against the schema migrations renders, at build time, with no
database running, and so the drift gate can pin the committed text byte for byte
against the renderer.
*/
package queries

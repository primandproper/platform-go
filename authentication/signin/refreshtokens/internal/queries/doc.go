/*
Package queries is the sign-in refresh token schema described as data: the
canonical table name, its columns in the order every read projects them, the
subsets a write assigns, and the six statements the store executes over them.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders them through database/querygen into the
canonical .sql files sqlc is run over; the store reads the same names through the
querier sqlc-gen-unison generates from those files. A column list spelled in both
places could differ in one name, and the symptom would be a check that passes
over SQL nobody executes.

# The six statements

  - InsertRefreshToken writes one mint, and serves both of them: the token a
    sign-in issues and the successor an exchange issues into the same family. It
    is a plain INSERT, so a digest collision is a failed write rather than a
    silently replaced row.
  - GetRefreshToken is the read-back an exchange makes when its guarded write
    matched nothing, keyed on the hash and the scope, projecting everything but
    the hash. It hides nothing, because what it exists to answer is which guard
    refused.
  - RedeemRefreshToken spends a token, guarded on the redemption not yet having
    happened, on the revocation not having happened, and on the deadline not
    having passed. Its row count is what decides who owns the token when two
    requests present one at once.
  - RevokeRefreshTokenFamily ends one login, which is what a detected reuse
    does.
  - RevokeRefreshTokensForSubject ends every login one person holds. It is not
    assembled out of family revocations, and [Render] says why it cannot be.
  - SweepRefreshTokens removes everything past its purge deadline, against a
    horizon the store binds from its own clock.

Three properties of the current statements are decisions rather than consequences
of the shapes they are rendered from, and [Render]'s comment argues each: the
projection that excludes the hash, the UTC binding SQLite's lexical comparison
depends on, and the deadline guard this corpus puts in the exchange's predicate
where its two nearest siblings make the same comparison in Go.

The rendered .sql files beside this one are the generator's output — see [Render]
and authentication/signin/refreshtokens/internal/queriesgen. Nothing imports them
and nothing executes them: they exist so `sqlc compile` can check these statements
against the schema migrations renders, at build time, with no database running,
and so the drift gate can pin the committed text byte for byte against the
renderer.
*/
package queries

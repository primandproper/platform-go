# The client contract

What a client of a `platform-go`-backed service must do, stated once, in no particular
language. `platform-client-ts` and `platform-client-swift` implement this; DDB's iOS app and
web frontend get it by using them.

It lives here because it describes **this module's wire behaviour**. A change to the sign-in
flow in v15 updates this file in the same pull request that makes the change, rather than two
client repositories discovering it afterwards.

**Status:** first draft against **v14.0.0**. The [open questions](#open-questions) are real
and unresolved; nothing should be implemented past them without a decision.

## Who this binds

A client here is anything holding credentials and calling a service: an iOS app, a browser, a
SvelteKit server route. Not another Go service — those use the module directly.

This document covers the **protocol**: what to send, what to do with what comes back, and in
what order. It says nothing about storage, transport or presentation, which differ per
platform and appear below only as [seams](#seams).

## Seams

Three things the contract requires but does not specify. Each client injects its own.

| seam | operations | iOS | browser |
| --- | --- | --- | --- |
| `CredentialStore` | `load()`, `save(token)`, `clear()` | Keychain | httpOnly cookie |
| `Clock` | `now()` | system | system |
| `Transport` | issue an RPC | `HTTP2ClientTransport` | gRPC-web / fetch |

`Clock` is a seam rather than a call to the system clock because expiry skew and refresh
timing are the parts worth testing, and a test that waits for real time is a test nobody runs.

`CredentialStore` holds a refresh token, which the proto describes as *"the one worth
stealing… it belongs in whatever the client's most protected store is, and it belongs nowhere
a log, a crash report or a URL can reach."* An implementation that cannot meet that bar should
not be written.

## Sign-in

`LoginForToken(Credentials)` returns an `IssuedToken`. `Credentials` carries `username` **or**
`email_address`, a `password`, and `totp_code`.

> Send `totp_code` whenever you have one. The proto: *"It is required from a user who holds a
> proven second factor and ignored for everybody else, so a client that always sends it when
> it has it is always correct."*

`IssuedToken` is the whole session:

| field | what a client does with it |
| --- | --- |
| `token` | the access credential; send it on every authenticated call |
| `expires_at` | when to stop using it — see [refresh](#the-access-token) |
| `refresh_token` | **single-use**; see [rotation](#refresh-token-rotation) |
| `refresh_token_expires_at` | the deadline that bounds the whole login |
| `active_account_id` | which account this is for |
| `family_id` | names this continuous login; the access token's `sid` claim |
| `token_id` | the `jti`; the only part safe to record |

**Do not parse the access token.** `active_account_id` and `family_id` are on the message
precisely so a client need not — the proto says so of `active_account_id`: *"It is here as well
as in the token's own claims so that a client which cannot parse the token still knows which
account it is holding one for."* A client that decodes the JWT to learn something already
handed to it has taken on the issuer's format as a dependency for no gain.

## The access token

Refresh when a call needs a token and `now() >= expires_at - skew`. Not on a timer, not in the
background.

| state | event | next | actions |
| --- | --- | --- | --- |
| `anonymous` | `login(credentials)` | `authenticating` | `LoginForToken` |
| `authenticating` | ok | `authenticated` | `save(token)`, serve waiters |
| `authenticating` | `UNAUTHENTICATED` | `anonymous` | surface, nothing to clear |
| `authenticated` | call needs a token, inside skew | `refreshing` | `ExchangeRefreshToken` |
| `authenticated` | call needs a token, outside skew | `authenticated` | use it |
| `refreshing` | ok | `authenticated` | `save(successor)`, serve **all** waiters |
| `refreshing` | `UNAUTHENTICATED` | `anonymous` | `clear()` |
| `refreshing` | any other error | `authenticated` | surface; **do not `clear()`** |

**R1 — one refresh at a time, and it is not an optimisation.** Concurrent callers finding an
expired token must await one in-flight exchange. Two exchanges means the same refresh token
presented twice, which is reuse, which revokes the family. Single-flight is *correctness* here,
not politeness.

**R2 — a failed refresh that is not `UNAUTHENTICATED` must not sign the user out.** A timeout
is not a revocation.

**R3 — one retry, never a loop.** An RPC returning `UNAUTHENTICATED` may trigger at most one
refresh-and-retry.

## Refresh token rotation

The rule that will bite anything written without reading it, quoted from the proto:

> It is single-use. Exchanging it spends it and returns its successor, and presenting one that
> was already spent ends the whole login — every token that sign-in issued stops working…
> **A client that retries an exchange must retry it with the successor it was given, never
> with the token it has already sent.**

So:

**R4 — never re-send a refresh token.** Not on timeout, not on transport error, not through a
generic retry policy. A blind retry of `ExchangeRefreshToken` is the one call in this API that
can destroy a working session.

**R5 — a refresh token is dead the moment it is sent.** Persist the successor before doing
anything else with it. A client that exchanges, crashes, and restarts holding the *old* token
has already lost: presenting it will revoke the family.

**R6 — `UNAUTHENTICATED` from an exchange means signed out, and you cannot learn why.**
Expired, reused and revoked are one answer by design. `ErrRefreshTokenReused`,
`ErrInvalidCredentials` and `ErrInvalidVerificationToken` all map to `codes.Unauthenticated`
with the message `"invalid credentials"` — byte-identical to a wrong password, because *"the
alternative is telling whoever presented it that their theft was noticed."* Clear credentials
and show a sign-in screen. Do not attempt to distinguish.

## Authenticated calls

Send `token` as the bearer credential on every call. On `UNAUTHENTICATED`, apply R3: refresh
once, retry once, and on a second `UNAUTHENTICATED` go to `anonymous`.

`GetAuthStatus` is the one RPC that answers an anonymous caller instead of refusing it —
*"'am I signed in' is a question whose answer can be no"* — so it is safe to call before
knowing whether credentials are good. It returns `authenticated: bool` and an `AuthStatus`
present only when true.

## Errors

Two codes carry most of the meaning:

- **`UNAUTHENTICATED`** — not signed in, or no longer. Includes wrong password, second factor
  required, expired/reused refresh token, invalid verification link.
- **`PERMISSION_DENIED`** — proven, and refused anyway: banned, terminated, not an
  administrator, admin sign-in disabled. The proto's mappers are explicit that these are *not*
  `UNAUTHENTICATED`, because *"a sign-in answering PermissionDenied would send a
  retry-with-credentials path down the give-up branch."*

A client must branch on the code for this distinction: `UNAUTHENTICATED` means offer
credentials again; `PERMISSION_DENIED` means stop asking.

## Pagination

List RPCs take a `QueryFilter` and return a page. Cursor-based:

- request: `cursor`, `max_response_size`, `sort_by`, `include_archived`, and
  `created_after`/`created_before`/`updated_after`/`updated_before`
- response: `cursor`, `previous_cursor`, `filtered_count`, `total_count`, `counts_known`

**R7 — treat cursors as opaque.** Do not parse, derive, or construct one.

**R8 — `counts_known` gates the counts.** When false, `filtered_count` and `total_count`
vouch for nothing and a UI must not render "of N". This is not pedantry: a store whose counts
ride along on its rows, handed an empty final page, has no row to read them off — so a client
walking a keyset to its end sees a `0` that is *not* a result. `counts_known` is the field
that tells an honest zero from an absent one, and it defaults to false.

## Open questions

These are unresolved. Both clients must answer them the same way, so they are decisions rather
than implementation details.

**Q1 — What does a client do when `ExchangeRefreshToken` fails ambiguously?** A timeout or
dropped connection leaves the client unable to know whether the token was spent. R4 forbids
re-sending it; but there is no successor to retry with, because none arrived. The options are
to sign out immediately (losing sessions to a flaky network) or to re-send and risk revoking a
live family (R4's exact prohibition). **This needs an answer before either client ships**, and
it probably needs a server-side affordance — an idempotent exchange, so a repeat of the *same*
request is distinguishable from a genuine reuse.

That affordance is **additive**, and so is a minor release rather than a major one. A new
optional field on `ExchangeRefreshTokenRequest` is wire-compatible, and the behaviour only
differs when a client sends it. The one thing that would force a major is the storage it
needs: `RefreshTokenStore` is exported and injected through `WithRefreshTokenStore`, so it is
explicitly meant to be implemented outside this module, and adding a method to it would break
every external implementation at compile time. The way out is the usual one — declare a new
interface that embeds `RefreshTokenStore`, type-assert for it at runtime, and let a store that
does not implement it behave exactly as it does today. Idempotency then arrives as an opt-in
capability, and nobody's build breaks.

**Q2 — Is the second-factor signal a contract?** `ErrSecondFactorRequired` and
`ErrInvalidCredentials` deliberately share one code and differ only in message — *"one says
try again, the other says ask for a code. Splitting them into two codes would hand a client a
branch that is also an oracle."* A client that must show a TOTP prompt therefore has to branch
on message text, which is not a stable interface. Either the message becomes part of the
contract, or something non-oracular needs to carry it.

**Q3 — Idempotency keys.** No v14 proto defines a client-supplied idempotency key —
`grep -r idempotency --include='*.proto'` over this module returns nothing — yet R3 and R4
describe retrying. Retries of non-idempotent RPCs need one.
Confirm whether a convention exists; if not, it belongs in the module rather than in each
client. Adding one is additive — a new field on a request message — so it needs a minor
release, not a major.

**Q4 — Streams.** Nothing here covers reconnect, backoff or resumption for streaming RPCs.

## Non-goals

No UI. No product protos — a product's own services generate clients in the product's
repository; this covers `platform-go`'s. No retrying a non-idempotent call without an answer
to Q3.

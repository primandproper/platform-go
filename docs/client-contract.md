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

**Q1 — An ambiguous `ExchangeRefreshToken` failure. Decided; pending #869.** A timeout or
dropped connection leaves a client unable to know whether its token was spent. R4 forbids
re-sending it, and no successor arrived to retry with, so both available moves lose a working
session.

The answer is an idempotency key. Once #869 lands, the rule for a client is:

**R9 — retry an ambiguous exchange with the same idempotency key, and expect a different
successor.** Mint the key once per logical exchange, outside the retry loop, and send it on
every attempt. The service recognises the repeat, mints a *fresh* successor and revokes the
one the first attempt produced. A client that receives a token it has not seen before has not
found a bug: re-minting is the design, chosen over replaying a stored response so that no
refresh token secret is ever kept at rest, and so that an attacker replaying the request
revokes the real client's token — making theft visible — instead of silently sharing it.

Until #869 lands there is no safe retry, and a client must treat an ambiguous exchange
failure as a lost session.

**Q2 — The second-factor signal. Decided: keep the shared code, fix the encoding.**
`ErrSecondFactorRequired` and `ErrInvalidCredentials` continue to share
`codes.Unauthenticated`, because any machine-readable distinction between them *is* the
oracle: it confirms that a supplied password was correct. The attacker who learns that cannot
reach the account — the second factor holds — but they leave with a verified credential pair,
and people reuse passwords, so the harm lands mostly on the user's other accounts rather than
on this service.

That disclosure is accepted, bounded by rate limiting on the sign-in path. What is not
accepted is carrying the distinction in an English message a client has to match on. The
signal moves to structured status details, so a client can branch on a code without the
response becoming an oracle to anyone who did not already have one.

**R10 — a client shows a second-factor prompt on the structured detail, never on message
text.** Until that detail exists, a client that must distinguish the two has no stable
interface and should prompt for a code on any `UNAUTHENTICATED` from sign-in rather than
parse prose.

Worth recording against a future revisit: this module closes its other existence oracles
deliberately. `RequestMagicLinkResponse` is an empty message, and its documentation requires
the same silence of consumers — *"answering a known address with a 200 and an unknown one
with a 404 puts the oracle back in their transport."* So sign-in's password oracle is not one
open door among many; it is close to the last one. If the data here ever becomes worth more
to an attacker, the alternative is to issue a second-factor challenge on **every** sign-in
attempt, valid password or not, with identical timing — machine-readable and non-oracular, at
the cost of prompting for a code before telling somebody they mistyped.

**Q3 — Idempotency keys. Resolved: the convention exists.** I looked in the wrong place — it
is not a proto field, it is gRPC metadata, which is the right home for it. `primitives-go`'s
`idempotency` package defines `MetadataKey = "idempotency-key"` and ships both a client and a
server interceptor, and this module already uses them in `settings/grpc/client`,
`identity/grpc/client` and `saga`.

The rule for a client, from that package's own documentation:

> The client generates one before its first attempt and reuses that same value on every retry
> of the same logical operation… A key minted inside the retry loop is a new key per attempt,
> which looks like protection and provides none.

So: `idempotency.WithNewKey(ctx)` once, **outside** the retry loop, and the client
interceptor forwards it. A call carrying no key is sent exactly as it would be without the
interceptor, so this is opt-in per call rather than imposed. Both clients must send
`idempotency-key` on retryable non-idempotent calls, minted once per logical operation.

**Q4 — Streams.** Nothing here covers reconnect, backoff or resumption for streaming RPCs.

## Non-goals

No UI. No product protos — a product's own services generate clients in the product's
repository; this covers `platform-go`'s. No retrying a non-idempotent call without an answer
to Q3.

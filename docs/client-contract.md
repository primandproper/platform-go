# The client contract

What a client of a `platform-go`-backed service must do, stated once, in no particular
language. `platform-client-ts` and `platform-client-swift` implement this; DDB's iOS app and
web frontend get it by using them.

It lives here because it describes **this module's wire behaviour**. A change to the sign-in
flow in v15 updates this file in the same pull request that makes the change, rather than two
client repositories discovering it afterwards.

**Status:** describes **v14** as it stands. Every rule here is behaviour a client can rely on
today; nothing below is aspirational, and the one thing deliberately left out is named under
[keeping this true](#keeping-this-true).

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
| `refreshing` | any other error | `authenticated` | retry once with the same key (R10); surface; **do not `clear()`** |

**R1 — one refresh at a time, and it is not an optimisation.** Concurrent callers finding an
expired token must await one in-flight exchange. Two exchanges means the same refresh token
presented twice, which is reuse, which revokes the family. Single-flight is *correctness* here,
not politeness.

**R2 — a failed refresh that is not `UNAUTHENTICATED` must not sign the user out.** A timeout
is not a revocation.

**R3 — one retry, never a loop.** An RPC returning `UNAUTHENTICATED` may trigger at most one
refresh-and-retry.

**R4 — mint an idempotency key once per logical operation, outside the retry loop.**
`primitives-go`'s `idempotency` package defines `MetadataKey = "idempotency-key"` and ships a
client interceptor that forwards whatever is on the context; this module already sends it
from `settings/grpc/client` and `identity/grpc/client`. Send it on any non-idempotent call
you are willing to retry. From that package's own documentation: *"A key minted inside the
retry loop is a new key per attempt, which looks like protection and provides none."* A call
carrying no key is sent exactly as it would be without the interceptor, so this is opt-in per
call.

## Refresh token rotation

The rule that will bite anything written without reading it, quoted from the proto:

> It is single-use. Exchanging it spends it and returns its successor, and presenting one that
> was already spent ends the whole login — every token that sign-in issued stops working…
> **A client that retries an exchange must retry it with the successor it was given, never
> with the token it has already sent.**

So:

**R5 — never re-send a refresh token bare.** Not on timeout, not on transport error, not
through a generic retry policy. A blind retry of `ExchangeRefreshToken` is the one call in this
API that can destroy a working session. The single exception is a retry carrying the same
idempotency key the lost attempt carried, which is
[R10](#recovering-a-lost-exchange-and-telling-refusals-apart) and is the only reason that rule
exists.

**R6 — a refresh token is dead the moment it is sent.** Persist the successor before doing
anything else with it. A client that exchanges, crashes, and restarts holding the *old* token
has already lost: presenting it will revoke the family.

**R7 — `UNAUTHENTICATED` from an exchange means signed out, and you cannot learn why.**
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
credentials again; `PERMISSION_DENIED` means stop asking. `FAILED_PRECONDITION` and
`INVALID_ARGUMENT` carry the rest — the state is wrong, or the request was. Which refusal
produced any of them is [R11](#recovering-a-lost-exchange-and-telling-refusals-apart)'s table.

## Pagination

List RPCs take a `QueryFilter` and return a page. Cursor-based:

- request: `cursor`, `max_response_size`, `sort_by`, `include_archived`, and
  `created_after`/`created_before`/`updated_after`/`updated_before`
- response: `cursor`, `previous_cursor`, `filtered_count`, `total_count`, `counts_known`

**R8 — treat cursors as opaque.** Do not parse, derive, or construct one.

**R9 — `counts_known` gates the counts.** When false, `filtered_count` and `total_count`
vouch for nothing and a UI must not render "of N". This is not pedantry: a store whose counts
ride along on its rows, handed an empty final page, has no row to read them off — so a client
walking a keyset to its end sees a `0` that is *not* a result. `counts_known` is the field
that tells an honest zero from an absent one, and it defaults to false.

## Recovering a lost exchange, and telling refusals apart

Two rules that used to be worse than they should be. Both now have the server-side answer they
were waiting for, and both describe v14 as shipped.

**R10 — after an ambiguous `ExchangeRefreshToken` failure, retry with the same idempotency
key.** A timeout or a dropped connection leaves a client unable to know whether its token was
spent, and with no successor to retry with. Re-sending the token *bare* is still what R5
forbids. Re-sending it under the key that accompanied the first attempt is not: the server
recognises the retry, mints a **fresh** successor, and revokes the one the lost response
carried — so the client ends up holding exactly one live refresh token either way.

It is a re-mint, not a replay. Nothing recorded is handed back, and the token a retry receives
is a *different* one from whatever the first attempt minted. That is deliberate: a replay would
hand an attacker who captured the request the very token the legitimate client holds, two
parties sharing one credential, which is what reuse detection exists to prevent.

Four conditions, each of which a client either controls or can wait out:

- **The same key.** Mint it once per logical exchange, outside the retry loop, and send it as
  `idempotency-key` metadata — R4's rule, applied to the one call that most needs it. A key
  minted per attempt protects nothing.
- **One retry per key.** Honouring the retry clears the key, so a *second* presentation of that
  token and key is a reuse like any other and ends the family. Retry once; if that fails, sign
  in again. The bound is what keeps a captured request from minting live tokens for ten
  minutes.
- **Inside the window.** Ten minutes from the original exchange, and only while the successor
  is unspent — a client that got a successor through and used it has closed the window itself.
- **A well-formed key:** non-empty printable ASCII, no spaces, at most 255 bytes. A malformed
  one is refused with `INVALID_ARGUMENT` before the token is touched, so it costs a round trip
  rather than a session.

**A client that cannot know it is talking to a server with this must fall back to R5.** The
metadata is read by every `platform-go` sign-in service, but answering a keyed retry is the
refresh-token store's to do, and a service may inject its own. Against a store that does not,
a keyed retry is a bare retry: reuse, and the family revoked. The stores this module ships all
implement it.

**R11 — branch on the reason, never on the message.** `ErrSecondFactorRequired` and
`ErrInvalidCredentials` both answer `UNAUTHENTICATED` and differ in their wording, and a
message is not an interface — it can be reworded or localised without warning. Every refusal a
client may be told about therefore carries a `google.rpc.ErrorInfo` detail, at the standard
type URL, with `domain` `signin.platform-go.primandproper.github.com` and a stable
`UPPER_SNAKE_CASE` `reason`. Read the reason. It is chosen once and never reworded, which the
message explicitly is not, and it survives an edge that strips encoded error details.

| reason | code | what it means for a client |
| --- | --- | --- |
| `INVALID_CREDENTIALS` | `UNAUTHENTICATED` | offer credentials again |
| `SECOND_FACTOR_REQUIRED` | `UNAUTHENTICATED` | prompt for a code, resend with `totp_code` |
| `SECOND_FACTOR_NOT_ENROLLED` | `FAILED_PRECONDITION` | this door needs a second factor and the user has none; enrolling needs a sign-in they cannot have, so the remedy is the service's, not the client's |
| `USER_UNVERIFIED` | `FAILED_PRECONDITION` | registration is unfinished; send them to verification |
| `USER_SUSPENDED` | `PERMISSION_DENIED` | stop asking; the message carries the explanation and is meant to be shown |
| `USER_TERMINATED` | `PERMISSION_DENIED` | stop asking; unlike a suspension this does not reverse |
| `NOT_AN_ADMINISTRATOR` | `PERMISSION_DENIED` | stop asking |
| `ADMIN_SIGNIN_UNAVAILABLE` | `PERMISSION_DENIED` | stop asking |
| `NO_PASSWORD_CREDENTIAL` | `FAILED_PRECONDITION` | a signed-in subject changing a password they do not have; offer the door they do |
| `PASSWORD_ALREADY_SET` | `FAILED_PRECONDITION` | attaching a password to somebody who holds one; it is a change, not an attach |
| `NO_CREDENTIAL_NAMED` | `INVALID_ARGUMENT` | a registration that did not say how the user will sign in; fix the request |

That is the whole set, and its edges are both load-bearing. A refusal absent from it carries no
reason at all, which is how **R7 survives this**: a reused, expired or revoked refresh token
answers `INVALID_CREDENTIALS` here exactly as a wrong password does. The structured channel
does not reopen what the collapsed message closed, and a client still cannot learn why an
exchange failed.

`NOT_AN_ADMINISTRATOR` and `ADMIN_SIGNIN_UNAVAILABLE` are distinct over gRPC and collapsed over
HTTP. A client reading gRPC can tell a service with no administrative door from one whose door
it is not admitted through; both answers mean the same thing to a user.

## Keeping this true

A change to this module's wire behaviour updates this document in the pull request that makes
the change — that is the reason the document lives here rather than beside a client.

Nothing is outstanding. R10 and R11 were the two rules that named a ticket rather than a
behaviour, and both have landed: [#869](https://github.com/primandproper/platform-go/issues/869)
as the idempotent exchange, [#873](https://github.com/primandproper/platform-go/issues/873) as
the client-safe reason.

**Streams are parked deliberately.** Nothing here covers reconnect, backoff or resumption.
Designing that against an imagined workload would produce answers nobody could check, so it
waits for something that streams.

## Non-goals

No UI. No product protos — a product's own services generate clients in the product's
repository; this covers `platform-go`'s. No retrying a non-idempotent call without an
idempotency key on it (R4).

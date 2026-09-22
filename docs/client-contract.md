# The client contract

What a client of a `platform-go`-backed service must do, stated once, in no particular
language. `platform-client-ts` and `platform-client-swift` implement this; DDB's iOS app and
web frontend get it by using them.

It lives here because it describes **this module's wire behaviour**. A change to the sign-in
flow in v15 updates this file in the same pull request that makes the change, rather than two
client repositories discovering it afterwards.

**Status:** describes **v14.1.0**. Every rule here is behaviour a client can rely on against a
service built from that tag; nothing below is aspirational. Two rules — [R10](#recovering-a-lost-exchange-and-telling-refusals-apart)
and [R11](#recovering-a-lost-exchange-and-telling-refusals-apart) — need a server no older than
it, and each says so along with what a client does against one that is older. The one thing
deliberately left out is named under [keeping this true](#keeping-this-true).

## Who this binds

A client here is anything holding credentials and calling a service: an iOS app, a browser, a
SvelteKit server route. Not another Go service — those use the module directly.

This document covers the **protocol**: what to send, what to do with what comes back, and in
what order. It says nothing about storage, transport or presentation, which differ per
platform and appear below only as [seams](#seams).

## Seams

Four things the contract requires but does not specify. Each client injects its own.

| seam | operations | iOS | browser |
| --- | --- | --- | --- |
| `CredentialStore` | `load()`, `save(token)`, `clear()` | Keychain | httpOnly cookie |
| `Clock` | `now()` | system | system |
| `Transport` | issue an RPC; the authority it dials | `HTTP2ClientTransport` | gRPC-web / fetch |
| `Authorizer` | `credentials(token)` | `authorization: Bearer <token>` | the same |

`Clock` is a seam rather than a call to the system clock because expiry skew and refresh
timing are the parts worth testing, and a test that waits for real time is a test nobody runs.

`CredentialStore` holds a refresh token, which the proto describes as *"the one worth
stealing… it belongs in whatever the client's most protected store is, and it belongs nowhere
a log, a crash report or a URL can reach."* An implementation that cannot meet that bar should
not be written.

`Authorizer` is a seam and not a rule because **nothing in this module fixes how an access
token reaches a server.** A service turns a token back into a caller through a
`callers.PrincipalExtractor` the consumer writes — the schema's own words are that *"turning
one back into a caller is the consumer's interceptor"* — so there is no metadata key this
document could name as *the* answer. What it can name is the default a client implements
unless a deployment says otherwise: the metadata entry `authorization`, valued
`Bearer <token>`, on the five authenticated RPCs and on `GetAuthStatus` whenever a token is
held.

The other seven anonymous RPCs carry no credential at all, and `ExchangeRefreshToken` is the
one worth saying twice: it authenticates with the refresh token in its body, never with the access
token, so a client that waits for a valid access token before refreshing has it backwards.

### The tenant travels on the connection

A multi-tenant service resolves *whose directory this is* before anybody has proved anything,
so it cannot come off a principal and deliberately cannot come off the request — the schema
reserves the field name so there cannot be one. It comes off the connection, through a
`signingrpc.ScopeResolver` the consumer supplies, and what on the connection carries it is
theirs to choose: a host, a metadata entry, or a client certificate. A deployment that names
no resolver puts every request in `tenancy.Global`, which is exactly what a single-tenant
application wants.

None of the three is a trust boundary, and a client should not read one into it. `:authority`
is a client-set pseudo-header and a metadata entry is client-set too, so naming a tenant buys
the directory a password is checked against and admits nobody; only a client certificate binds
the scope to something a caller cannot simply claim. The choice is operational, which is what
makes it safe to leave to the deployment — and why a client covers all three rather than
picking one.

Two of the three are already the `Transport` seam: a host is the authority it dials, and a
certificate is its TLS configuration. The third is one value, so a client takes **a set of
constant metadata entries sent on every call**. A browser needs neither, because it is served
from the tenant's own host and dials its own origin; an iOS app cannot derive a host from an
email address and a password, so whichever mechanism the deployment chose arrives out of band
— a managed setting, an onboarding link, an organisation code typed once.

**R12 — whatever carries the tenant travels identically on every call, anonymous and
authenticated alike.** `WithScopeResolver` is explicit about the cost of getting this wrong: a
token minted in one directory and presented on a connection that resolves to another is
refused by *finding nobody*, because every read filters on the scope it was handed. That is the
safe direction and a baffling one to debug, and no error says which of the two was wrong.

## Sign-in

Three RPCs mint a session, and they differ in what was proven rather than in what they
produce: `LoginForToken`, `AdminLoginForToken` — the administrative door, typically a shorter
lifetime, and a separate message so the two doors can diverge — and `RedeemMagicLink`, which
answers a mailed link. All three answer with an `IssuedToken`, and everything below is true of
all three.

`Credentials` carries `username` **or** `email_address` (both is refused and neither is
refused), a `password`, a `totp_code`, and an `active_account_id`. `RedeemMagicLink` carries
the link's `token` in place of the handle and password, and still carries `totp_code` and
`active_account_id`.

> Send `totp_code` whenever you have one. The proto: *"It is required from a user who holds a
> proven second factor and ignored for everybody else, so a client that always sends it when
> it has it is always correct."*

`active_account_id` is which account the minted token is for, and empty means the user's
default. There is **no RPC that re-points a live token at another account**:
`AuthStatus.account_ids` lists every account the caller is a live member of, and moving to one
of them is a fresh sign-in naming it — a new token, a new family. `identity.SetDefaultAccount`
is not that door and a client building a switcher out of it will be surprised: it changes the
caller's *landing* account, which is where the next sign-in goes when no account is named, and
leaves the token in hand pointing exactly where it did. An account the user is not a live
member of is refused rather than honoured, which is *"the check that stops a client choosing
whose data its token reaches."*

`IssuedToken` is the whole session:

| field | what a client does with it |
| --- | --- |
| `token` | the access credential; send it on every authenticated call |
| `expires_at` | when to stop using it — see [refresh](#the-access-token) |
| `refresh_token` | **single-use**; see [rotation](#refresh-token-rotation) |
| `refresh_token_expires_at` | the deadline that bounds the whole login |
| `active_account_id` | which account this is for |
| `administrative` | whether this came through the administrative door |
| `family_id` | names this continuous login; the access token's `sid` claim |
| `token_id` | the `jti`; the only part safe to record |

A service that stores no refresh tokens leaves `refresh_token` and
`refresh_token_expires_at` empty, which is a valid shape rather than an error: such a client
holds an access token until it expires and then signs in again. `family_id` is populated
either way.

**Do not parse the access token.** `active_account_id` and `family_id` are on the message
precisely so a client need not — the proto says so of `active_account_id`: *"It is here as well
as in the token's own claims so that a client which cannot parse the token still knows which
account it is holding one for."* A client that decodes the JWT to learn something already
handed to it has taken on the issuer's format as a dependency for no gain.

## The access token

Refresh when a call needs a token and `now() >= expires_at - skew`. Not on a timer, not in the
background. `skew` is the client's number rather than the server's, and this document fixes it
at thirty seconds so that two clients do not differ over a value neither can derive. It is the
reason `Clock` is a seam.

| state | event | next | actions |
| --- | --- | --- | --- |
| `anonymous` | `load()` returned a token | `authenticated` | adopt it; the rules below decide the first call |
| `anonymous` | `load()` returned nothing | `anonymous` | offer sign-in |
| `anonymous` | `login(credentials)` | `authenticating` | one of the three doors |
| `authenticating` | ok | `authenticated` | `save(token)`, serve waiters |
| `authenticating` | `UNAUTHENTICATED`, reason `SECOND_FACTOR_REQUIRED` | `anonymous` | prompt for a code; resend the same credentials with `totp_code` |
| `authenticating` | `UNAUTHENTICATED`, anything else | `anonymous` | surface, nothing to clear |
| `authenticating` | `PERMISSION_DENIED` | `anonymous` | surface; stop asking (R11) |
| `authenticated` | call needs a token, `now() >= refresh_token_expires_at` | `anonymous` | `clear()`; there is no round trip to make |
| `authenticated` | call needs a token, inside skew | `refreshing` | `ExchangeRefreshToken` |
| `authenticated` | call needs a token, outside skew | `authenticated` | use it |
| `authenticated` | a call answered `UNAUTHENTICATED` | `refreshing` | `ExchangeRefreshToken`, once (R3) |
| `refreshing` | ok | `authenticated` | `save(successor)`, serve **all** waiters |
| `refreshing` | `UNAUTHENTICATED` | `anonymous` | `clear()` |
| `refreshing` | `PERMISSION_DENIED` | `anonymous` | `clear()`; stop asking |
| `refreshing` | ambiguous — R10's codes | `refreshing` | retry once with the same key (R10) |
| `refreshing` | any other error | `authenticated` | surface; **do not `clear()`** |

`PERMISSION_DENIED` on the refresh path is not hypothetical, and it is the row a client is most
likely to leave out. Every exchange re-reads the directory — *"a family that outlived a ban
would be a suspension that takes effect whenever the access token happens to expire"* — so a
user suspended mid-session is refused there, with `identity`'s own
`ErrSignInNotAdmitted` ("account status does not admit sign-in") and, unlike the sign-in door's
refusals, **no reason detail**. The login is over; retrying is a loop on a credential that will
keep being refused.

**R1 — one refresh at a time, and it is not an optimisation.** Concurrent callers finding an
expired token must await one in-flight exchange. Two exchanges means the same refresh token
presented twice, which is reuse, which revokes the family. Single-flight is *correctness* here,
not politeness.

**R2 — a failed refresh that is not `UNAUTHENTICATED` or `PERMISSION_DENIED` must not sign the
user out.** A timeout is not a revocation.

**R3 — one retry, never a loop.** An RPC returning `UNAUTHENTICATED` may trigger at most one
refresh-and-retry.

**R4 — mint an idempotency key once per logical operation, outside the retry loop.**
`primitives-go`'s `idempotency/grpc` package defines `MetadataKey = "idempotency-key"` and
ships a client interceptor that forwards whatever `idempotency.WithKey` put on the context;
this module already sends it from `settings/grpc/client` and `identity/grpc/client`. Send it on
any non-idempotent call you are willing to retry. From that package's own documentation: *"A
key minted inside the retry loop is a new key per attempt, which looks like protection and
provides none."* A call carrying no key is sent exactly as it would be without the interceptor,
so this is opt-in per call — and what a server does with one beyond `ExchangeRefreshToken`
depends on the deployment having installed the idempotency `Manager`, so a key is a
precondition for protection rather than protection itself.

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

Send the access token through the `Authorizer` seam on every authenticated call. On
`UNAUTHENTICATED`, apply R3: refresh once, retry once, and on a second `UNAUTHENTICATED` go to
`anonymous`.

`GetAuthStatus` is the one RPC that answers an anonymous caller instead of refusing it —
*"'am I signed in' is a question whose answer can be no"* — so it is safe to call before
knowing whether credentials are good. It returns `authenticated: bool` and an `AuthStatus`
present only when true, and that message is where most of a client's obligations live. It is
read fresh rather than frozen into the token, which is the point: `IssuedToken` carries no user
and no permissions, because *"the alternative is a permission set frozen at sign-in, where
revoking a role has no effect until the token expires."*

| field | what a client owes |
| --- | --- |
| `user`, `active_account_id`, `account_ids` | who this is, where they are, and everywhere they could be |
| `requires_password_change` | an operator forced one. The service still signs them in — *"the alternative is a user who cannot reach the form"* — so routing them to it is the client's job |
| `email_address_verified` | false means an unfinished registration; the remedy is the mailed link |
| `has_password` | false is a passwordless user, and offering them a change-password form *"is offering them a form that cannot work"* |
| `two_factor_enrolled` | a secret issued and never verified is not one |

## Registration, and the two links

Registration is sign-in's, because sign-in is what hashes a password. Four RPCs matter to a
client and their authority differs sharply:

- **`Register` requires a caller**, and a client is not one. *"An open sign-up is a flow with
  policy in it, a captcha, a rate limit, an email domain rule, and this service holds none of
  that"* — so a public sign-up screen calls the consumer's own registrar, which calls this.
  A registration names the credential the registrant chose as a `oneof`, and one naming neither
  arm is refused rather than read as passwordless (`NO_CREDENTIAL_NAMED`).
- **`VerifyEmailAddress`** and **`AttachPassword`** are anonymous and carry the token the
  mailed link carried, which is the whole of their authority. There is no verification token in
  any response — it travels to the person it is about, never back to whoever called `Register`.
- **`RequestMagicLink`** is anonymous and answers *identically* whether the address exists or
  not, padding its own timing so the two cannot be told apart by a stopwatch. A client that
  renders "we sent it" on success and "no such account" on failure rebuilds the enumerator that
  padding exists to prevent. Show the same screen either way.

`AttachPassword` is not a password reset and a client should not offer it as one: it is refused
for an account that already holds a password, because it exists for somebody who registered
without one. Forgetting a password you have is [the next section](#resetting-a-forgotten-password).

## Resetting a forgotten password

A second service, on its own schema: `PasswordResetService`, in
`primandproper.platform.passwordreset.v1`. A client generates it beside the sign-in one and
dials it on the same connection, with the same tenant (R12) and the same `Transport` seam. All
three of its RPCs are anonymous and none of them can be anything else, because the whole premise
is somebody who cannot sign in.

It is the only way back for a person who has forgotten their password. `UpdatePassword` needs
the current one, and `AttachPassword` is refused for an account that already holds a password,
so a client without this flow has no path from "I forgot it" to a new password — a magic link
signs such a person in and still leaves them unable to change it.

| RPC | what a client does |
| --- | --- |
| `RequestPasswordReset(email_address)` | the "forgot password" form. Answers identically whoever holds the address |
| `VerifyPasswordResetToken(token)` | the page load behind the link, *before* rendering the form |
| `CompletePasswordReset(token, new_password)` | the form's submit |

**R15 — show the same screen whether the address exists or not.** The response is empty, and it
is empty so that it cannot differ; the service also holds its own answer to a floor so the two
cannot be told apart by a stopwatch. A client that renders "check your inbox" on success and "no
account with that address" otherwise has rebuilt the account enumerator both of those exist to
prevent, in the one place the server cannot reach.

**R16 — verify before you render the form.** Nothing is held open by it and it decides nothing —
a token live when `VerifyPasswordResetToken` answered can be spent by somebody else a moment
later, and the answer that matters is `CompletePasswordReset`'s — but it is the difference
between telling somebody their link is dead now and telling them after they have chosen a
password. It answers `expires_at`, and nothing else: not who the link is for, deliberately, so a
forwarded link does not name the account it opens.

**A dead link is three refusals told apart, and they are told apart by their message.** Expired,
already used, and never a link all answer `FAILED_PRECONDITION`, and `passwordreset` registers
no reasons — which is correct rather than an omission, and is R11's rule applied rather than
broken. A reason exists for a refusal a client must *act* on differently; all three of these
have one remedy, which is to ask for a new link. So a client shows the message and offers that
button, for all three, and still does not branch on the text.

**Completing a reset signs nobody in.** There is no token in the response and there will not be
one; the next call is `LoginForToken` with the password that was just chosen. And it does not
end the sessions that account already has — which is the same thing `UpdatePassword` does, and
is the consumer's to change. A client whose user is resetting *because* they think somebody else
is in their account should sign them in afterwards and call `SignOutEverywhere`, which is the one
sequence that actually ends the other sessions.

## Signing out

Two RPCs, and a client wants both. `SignOut` carries the refresh token and needs no caller;
`SignOutEverywhere` requires a caller and takes no fields, ending every login that person holds
on every device.

`SignOut` is anonymous deliberately, and it is the half a client gets wrong by being tidy: an
application that has been closed for a week has an expired access token, which is exactly when
somebody presses the button, so the credential that names the login is the refresh token and not
a caller. Send the refresh token, then `clear()`, in that order — a client that clears first has
thrown away the only thing that could end the login.

**Every refusal a presented token can draw is a success.** Unknown, already spent, already
revoked and expired all mean the same thing about the login being ended, and the service answers
all of them with an empty `SignOutResponse`. So a client never shows an error for signing out,
and never needs to: pressing it twice, or pressing it on a session that had already lapsed, is
the ordinary case.

**What neither one stops is an access token already issued.** Nothing can — it is checked
against the issuer's signature rather than against any table — so a sign-out takes effect
within one access-token lifetime. A client should therefore `clear()` locally as well, which it
was going to do anyway, and a deployment that needs the window shorter shortens the access
token.

**R17 — a `signOut()` that only clears local state is a lie on a shared device.** It is one
extra call, it cannot fail in a way worth reporting, and without it the refresh token stays
exchangeable for the rest of its window by whoever has the device.

An operator ending somebody else's sessions is not here and will not be: those RPCs name
nobody, so there is no field an administrator could use. That act is a Go-side call behind the
consumer's own administrative surface.

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
produced any of them is [R11](#recovering-a-lost-exchange-and-telling-refusals-apart)'s table,
where there is one.

**R13 — read the reason where there is one, and branch on the code where there is not.** A
reason is absent more often than R11's table suggests, for three unrelated reasons, and a
client that treats its absence as a bug has a client that breaks on a Tuesday:

- **Most of the module gives none.** Sign-in is the only package that registers reasons.
  `identity` has thirteen refusals a caller may be told about and no identifiers for any of
  them, so an `ExchangeRefreshToken` refused because the user was suspended arrives as a bare
  `PERMISSION_DENIED`.
- **The detail is attached best-effort.** The server-side interceptor builds the status and
  adds the reason only if it marshals, on the grounds that *"a status that says the right code
  and message without a detail is better than an error about attaching one."*
- **A deployment may not have registered anything.** The mappers reach a client only once
  `errormappers.Register` has been called. Without it, a refusal that wraps a platform sentinel
  still maps — `MapToGRPC` consults `PlatformMapper` before any registered mapper — and every
  refusal one of this module's own packages raises on its own account arrives as `codes.Unknown`,
  which for a sign-in *"means a client cannot tell 'wrong password' from 'the database is
  down'"*. A client cannot fix that and should fail legibly rather than mysteriously when it
  sees it.

The detail rides in the gRPC status's details, which on the wire is the `grpc-status-details-bin`
trailer carrying a `google.rpc.Status` whose `details` are `Any`-packed; the one to look for is
`type.googleapis.com/google.rpc.ErrorInfo`. Both clients need `google/rpc/status.proto` and
`google/rpc/error_details.proto` generated to read it — neither has them today, because both
fetch scripts copy `*/proto/primandproper/*` and these are Google's. A second detail carries
the whole error chain and is the *peer's* rather than the client's: an edge reachable by
untrusted clients strips it, which is exactly why the reason is a separate detail.

## Pagination

List RPCs take a `QueryFilter` and answer with a `Pagination` beside their rows. Cursor-based:

- request: `cursor`, `max_response_size`, `sort_by`, `include_archived`, and
  `created_after`/`created_before`/`updated_after`/`updated_before`. Every field is optional and
  an absent one filters nothing, so the empty message asks for the default page.
- response: `cursor`, `previous_cursor`, `filtered_count`, `total_count`, `counts_known`,
  `max_response_size`, and `applied_query_filter` — *"the filter this page was answered with,
  after defaults and bounds — not necessarily the one that was sent."*

`sort_by` is `"asc"` or `"desc"` and nothing else: it is a direction, not a column. On the usual
list that is oldest first and newest first. `max_response_size` above the server's ceiling is
**clamped rather than refused**, so the page size that was applied is the one in the response,
not the one you asked for.

**R8 — treat cursors as opaque.** Do not parse, derive, or construct one. The two are
directional and are not the same value: `previous_cursor` is the one that reached this page, so
an empty one means the first page, and `cursor` is the one that reaches the next.

**R9 — `counts_known` gates the counts.** When false, `filtered_count` and `total_count`
vouch for nothing and a UI must not render "of N". This is not pedantry: a store whose counts
ride along on its rows, handed an empty final page, has no row to read them off — so a client
walking a keyset to its end sees a `0` that is *not* a result. `counts_known` is the field
that tells an honest zero from an absent one, and it defaults to false. When true, both
describe the collection the page was cut from rather than the page, which is why they do not
shrink as a caller walks it.

**R14 — walk until a page comes back with no rows.** `cursor` is the last row's identifier, so
it *"is empty only when this page held no rows, and it says nothing about whether a further page
exists: a full page and the final page carry an equally non-empty cursor."* Two consequences,
and the second is the one that produces missing data. Reaching the end costs one extra
round trip, which is the shape of a keyset walk rather than a bug. And **a short page is not
the end** — nothing in the schema promises a page smaller than `max_response_size` is the last
one, so a client that stops on one has invented a guarantee and will silently truncate a list.

## Recovering a lost exchange, and telling refusals apart

Two rules that arrived with v14.1.0. Against a server older than that they are not merely
unavailable — R10 is actively destructive and R11 reads as a permanent absence — so each says
what to do instead.

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

Ambiguity is a code list, because "the answer may or may not have arrived" has to be something
a client can test:

| outcome | ambiguous? |
| --- | --- |
| `DEADLINE_EXCEEDED`, `UNAVAILABLE`, `CANCELLED`, or a transport failure with no status | yes — retry once, same key |
| `UNAUTHENTICATED` | no — the exchange was refused; sign out (R7) |
| `PERMISSION_DENIED` | no — the directory refused them; sign out |
| `INVALID_ARGUMENT` | no — the request was refused before the token was touched, a malformed key being the usual cause. Correct it; the next attempt is a *first* attempt, not a retry |
| `INTERNAL`, `UNKNOWN` | treat as ambiguous; the call may have committed before failing |

Deadlines are the client's, and this is where the choice shows: a short deadline on the
exchange converts slow successes into ambiguous failures, and every one of those costs the
round trip R10 exists to make survivable.

Four conditions, each of which a client either controls or can wait out:

- **The same key, sent on the first attempt.** A key that was not on the original request
  cannot be recognised on the retry — it is recorded by the same `UPDATE` that spends the
  token — so the exchange always carries one, not just the attempts a client expects to lose.
  Mint it once per logical exchange, outside the retry loop, and send it as `idempotency-key`
  metadata. A key minted per attempt protects nothing.
- **One retry per key.** Honouring the retry clears the key, so a *second* presentation of that
  token and key is a reuse like any other and ends the family. Retry once; if that fails, sign
  in again. The bound is what keeps a captured request from minting live tokens for ten
  minutes.
- **Inside the window.** Ten minutes from the original exchange, and only while the successor
  is unspent — a client that got a successor through and used it has closed the window itself.
- **A well-formed key:** non-empty printable ASCII, no spaces, at most 255 bytes. A malformed
  one is refused with `INVALID_ARGUMENT` before the token is touched, so it costs a round trip
  rather than a session.

**R10 is in-process, and deliberately does not survive a restart.** The key lives in memory
beside the in-flight exchange. A process that was *suspended* and resumed still holds it, which
is the case the ten-minute window was sized for — the store's own words are that it is *"sized
for an app that was backgrounded and reopened rather than for a dropped packet."* A process
that was **killed** has lost it, and is back to holding a token it may not re-send: that is R5,
and it is a sign-out. Persisting the key instead would mean a pending-exchange record whose own
crash and cleanup semantics need the same care as the thing it protects, to cover a narrower
case than the one already covered.

**A client that cannot know it is talking to a server with this must fall back to R5.** Two
things have to be true. The server must be built from v14.1.0 or later: the metadata is read by
every `platform-go` sign-in service, but answering a keyed retry is the refresh-token store's to
do, and that store gained the behaviour in v14.1.0. And a service may inject its own store;
against one that does not implement it, a keyed retry is a bare retry — reuse, and the family
revoked. The stores this module ships all implement it.

**R11 — branch on the reason, never on the message.** `ErrSecondFactorRequired` and
`ErrInvalidCredentials` both answer `UNAUTHENTICATED` and differ in their wording, and a
message is not an interface — it can be reworded or localised without warning. Every
**sign-in** refusal a client may be told about therefore carries a `google.rpc.ErrorInfo`
detail, at the standard type URL, with `domain` `signin.platform-go.primandproper.github.com`
and a stable `UPPER_SNAKE_CASE` `reason`. Read the reason. It is chosen once and never
reworded, which the message explicitly is not, and it survives an edge that strips encoded
error details. Everything outside sign-in is [R13](#errors): the code, and nothing finer.

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

That is the whole set, and its edges are both load-bearing. A sign-in refusal absent from it
carries no reason at all, which is how **R7 survives this**: a reused, expired or revoked
refresh token answers `INVALID_CREDENTIALS` here exactly as a wrong password does. The
structured channel does not reopen what the collapsed message closed, and a client still cannot
learn why an exchange failed.

`NOT_AN_ADMINISTRATOR` and `ADMIN_SIGNIN_UNAVAILABLE` are distinct over gRPC and collapsed over
HTTP. A client reading gRPC can tell a service with no administrative door from one whose door
it is not admitted through; both answers mean the same thing to a user.

Against a server older than v14.1.0 there are no reasons on any refusal, and the one branch
that actually matters has no code to read: a second-factor prompt and a wrong-password screen
are both `UNAUTHENTICATED`. A client cannot resolve that from `AuthStatus` either — whether a
user is enrolled is a fact only a signed-in caller can read, and this caller is not one. So the
answer is to need no branch: put an optional code field on the sign-in form, send whatever is in
it, and let the refusal mean "these credentials, whatever you typed, were not enough." Matching
the message is the other option and is what R11 exists to stop.

## Keeping this true

A change to this module's wire behaviour updates this document in the pull request that makes
the change — that is the reason the document lives here rather than beside a client. Where the
behaviour is new, the rule names the version that has it, because the clients pin a tag and a
rule that describes unreleased `main` is a rule that breaks a session.

**Streams are parked deliberately.** Nothing here covers reconnect, backoff or resumption,
which costs nothing today: no `.proto` in this module declares a streaming RPC. Designing that
against an imagined workload would produce answers nobody could check, so it waits for
something that streams.

## Non-goals

No UI. No product protos — a product's own services generate clients in the product's
repository; this covers `platform-go`'s. No retrying a non-idempotent call without an
idempotency key on it (R4). No administrative surface: every RPC this document covers is about
the caller or about the credential the caller presented, and an operator acting on somebody else
goes through a consumer's own service.

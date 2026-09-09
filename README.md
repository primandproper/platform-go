# platform-go

[![Go Reference](https://pkg.go.dev/badge/github.com/primandproper/platform-go/v14.svg)](https://pkg.go.dev/github.com/primandproper/platform-go/v14) [![codecov](https://codecov.io/github/primandproper/platform-go/graph/badge.svg?token=69RLLWLJ39)](https://codecov.io/github/primandproper/platform-go)

A Go library of the things a product has: a noun with a table, its lifecycle, its transport, its permissions and its privacy obligations. Identity, billing, audit, webhooks, sagas and the rest ship a `Store`, the DDL for the dialects they serve, a mock, and — for the ones that have crossed — a gRPC or HTTP surface over them. The infrastructure they are built from is [`primitives-go`](https://github.com/primandproper/primitives-go), which this module requires; [Primitives and Domains](#primitives-and-domains) is the rule that says which is which.

**Module:** `github.com/primandproper/platform-go/v14`
**Go:** 1.27

## Project Status & Stability

> **`main` is not a release channel.** Anything on `main` that has not been cut into a tagged release is considered under active development — alpha/beta, unstable, and unsupported. Treat it as such.

This repository follows a deliberately conservative release model:

- **Only tagged releases are supported.** If it isn't behind a version tag, it can change or break without notice, and no support or compatibility is promised for it.
- **`main` moves ahead of the latest release.** New work — including breaking changes — lands on `main` well before it is deemed release-worthy. Two facts locate you at any moment, and both are derived rather than written down here: the module path in `go.mod` is the major that `main` is currently building toward, and the highest version tag is the latest supported release. Whatever is on `main` but not yet in that tag is subject to change — and immediately after a major bump, that is the entire major.
- **Semantic Versioning, enforced by Go's module paths.** Breaking changes increment the major version and the module import path (`/vN` → `/vN+1`), so a major bump can never silently break a consumer that hasn't opted in. The path bump lands in the same change that makes the break, never as a follow-up, which is why `main`'s major is frequently one ahead of anything you can fetch by tag.
- **No stability guarantees on unreleased APIs.** Interfaces, config shapes, and package boundaries on `main` are subject to change until they ship in a release.

If you depend on this library, pin to a released tag — and note that `@latest` against a major that has no tag yet resolves to a commit on `main` rather than to a release. If you want to track upcoming work, `main` is fair game — just don't expect it to hold still.

## Installation

```bash
go get github.com/primandproper/platform-go/v14@latest
```

Because breaking changes ride the major-version import path, upgrading across majors is an explicit, opt-in edit to your import paths — never a surprise from `go get -u`.

## Design Patterns

**Store, DDL, mock.** A package that owns data ships a `Store` interface, a SQL implementation of it, the DDL for whichever dialects the matrix below grants it, and a moq-generated mock. Its statements are rendered into a checked corpus and executed through a generated querier, so a column renamed in a migration is a failed generate rather than a runtime scan error.

**A write takes a `Tx`; a read takes an executor.** Every exported store write reads `(ctx, tx database.Tx, scope tenancy.Scope, ...)` and every exported read reads `(ctx, q database.SQLQueryExecutor, scope tenancy.Scope, ...)`. A `Tx` is producible only inside `Client.WithTransaction`, so the write's signature is a compile-time claim that the caller is already in a transaction — which is the point, because a consumer's write almost never travels alone: the row, its audit entry and its outbox event are one fact. The read takes the wider type so that a caller inside a transaction sees its own uncommitted writes.

**Tenancy is a column, not a convention.** A component that stores consumer data stores it for somebody, and that somebody is a `tenancy.Scope` — an opaque owner identifier, deliberately not a `string`, so a scopeless call fails to compile rather than matching everything. `tenancy.Global()` is the scope of data belonging to no tenant, so a single-tenant application passes it everywhere and behaves exactly as it did before. Scope in the column, scope in the query, and no read path that omits it.

**Config subpackages.** Each package has a `config` subpackage with `env:`-tagged structs and `ValidateWithContext()` (via `go-ozzo/ozzo-validation`), and `logger`/`tracerProvider`/`metricsProvider` arrive as `WithX` options rather than positionally. Absent means noop: a caller that wants no observability names none of it. An unrecognized provider name returns `errors.ErrUnknownProvider` rather than a working-looking noop, because a typo that silently discards every message is a production incident that looks like a healthy process.

**OpenTelemetry throughout.** Every store, transport and worker here instruments through primitives-go's `observability`, whose logging, tracing, metrics and profiling pillars a consumer supplies once and threads everywhere.

**Error handling.** Uses [`cockroachdb/errors`](https://github.com/cockroachdb/errors) for rich, wrapped error context, over the sentinels primitives-go's `errors` package defines, conventionally imported as `platformerrors`. Its `errors/http` and `errors/grpc` map the primitives and cannot import the tier above them, so everything here maps itself: `authentication/oauth2clients`, `authentication/signin`, `dataprivacy`, `identity`, `links`, `operations` and `sessions` each export an `HTTPMapper` and a `GRPCMapper` beside their sentinels. The composition root registers all seven in one call — `errormappers.Register()`, which `service.Register` makes for a service built from a `service.Config` and a service assembled by hand makes itself. `operations/http.New` is the single exception, registering its own HTTP mapper because it is the only surface here that both answers through `errors/http` and belongs to a package on that list. `internal/sentinelmatrix` checks that every exported sentinel in those seven has a decision recorded and that it still holds on both transports.

## Package Catalog

This module is the domain tier: a noun with a table, its lifecycle, its
transport, its permissions and its privacy obligations. The providers behind an
interface — `cache`, `database`, `email`, `messagequeue`, `observability`,
`routing`, `secrets`, `search` and the rest of what every service is built from
— are [`primitives-go`](https://github.com/primandproper/primitives-go), and its
README catalogues them. [Primitives and Domains](#primitives-and-domains) is the
rule that sorts a new package into one or the other.

Implementations are listed in parentheses. Where an implementation is a SQL
dialect, [SQL Dialect Support](#sql-dialect-support) is the full matrix and the
reasons behind the three exceptions.

### Identity & access
| Package                                | Purpose                                                                    | Implementations         |
|----------------------------------------|----------------------------------------------------------------------------|-------------------------|
| `identity`                             | Users, accounts, memberships and invitations, and the lifecycle over them   | postgres, mysql, sqlite (+ grpc) |
| `authentication/signin`                | Sign-in: the order the engines and the directory are used in, owning no table | — (+ grpc)            |
| `authentication/passwordreset`         | Password reset tokens: digest at rest, single use enforced by the store     | postgres, mysql, sqlite |
| `authentication/webauthn/database`     | Passkey ceremony state that outlives one replica                           | postgres, mysql, sqlite |
| `authentication/oauth2clients`         | An administered OAuth2 client registry                                     | postgres, mysql, sqlite (+ grpc) |
| `authentication/oauth2server/database` | The OAuth2 server's client and token tables                                | postgres, mysql, sqlite |
| `authorization/database`               | Roles and permissions as rows, behind the policy interface                 | postgres, mysql, sqlite |
| `sessions`                             | Server-side sessions over cookies                                          | cache, database (+ http) |

### Product & commerce
| Package        | Purpose                                                                                          | Implementations         |
|----------------|--------------------------------------------------------------------------------------------------|-------------------------|
| `billing`      | What a deployment sells, and what its customers paid: catalog, subscriptions, purchases, ledger  | postgres, mysql, sqlite |
| `entitlements` | Feature access & remaining quota                                                                 | —                       |
| `metering`     | Durable usage metering & quotas                                                                  | postgres, mysql, sqlite |
| `settings`     | Per-user and per-account runtime settings: admin-defined definitions, per-subject values         | postgres, mysql, sqlite |
| `comments`     | Threaded comments on consumer-declared targets                                                   | postgres, mysql, sqlite |
| `issuereports` | User-submitted issue reports with a triage lifecycle                                             | postgres, mysql, sqlite |
| `waitlists`    | Pre-launch waitlists: signup lifecycle, and an unsubscribe that outlives the address             | postgres, mysql, sqlite |
| `links`        | Signed, expiring, single-use action links                                                        | postgres, mysql, sqlite |

### Records, privacy & retention
| Package                  | Purpose                                              | Implementations         |
|--------------------------|------------------------------------------------------|-------------------------|
| `audit`                  | Tamper-evident audit log                             | postgres, mysql, sqlite |
| `dataprivacy`            | Subject access & erasure requests                    | postgres, mysql, sqlite |
| `cryptography/shredding` | Per-subject data keys that can be destroyed          | postgres, mysql, sqlite |
| `retention`              | Policy-driven expiry deletion                        | postgres, mysql, sqlite |
| `uploads/registry`       | Object metadata rows over an object store            | postgres, mysql, sqlite |

### Coordination & delivery
| Package         | Purpose                                                                             | Implementations         |
|-----------------|--------------------------------------------------------------------------------------|-------------------------|
| `outbox`        | Transactional outbox                                                                | postgres, mysql, sqlite |
| `workqueue`     | Leased work queue (`SKIP LOCKED` claim/complete/expire)                             | postgres                |
| `timers`        | Durable one-shot scheduling (run once at time T, fleet-wide)                        | postgres                |
| `operations`    | Long-running operations with durable state, two-tier progress, and streamed updates | postgres (+ http)       |
| `saga`          | Linear durable sagas with compensations                                             | postgres, mysql, sqlite |
| `webhooks`      | Outbound webhook delivery                                                           | postgres, mysql, sqlite |
| `notifications` | User notifications                                                                  | postgres, mysql, sqlite (+ async) |
| `search/sync`   | Reindexing worker driven by the outbox                                              | —                       |

### The composition root
| Package        | Purpose                                                                       |
|----------------|---------------------------------------------------------------------------------|
| `service`      | One walk of one config that wires both modules                                  |
| `errormappers` | The one call that tells the two transport registries what these sentinels mean  |

## Primitives and Domains

There were two kinds of package here and they have separated: the primitives
left for [`primitives-go`](https://github.com/primandproper/primitives-go), and
what stays is the domain tier. The rule that sorted them is the one to check a
new package against before writing it, because it now decides which of the two
repositories the package is written for, and it is one property — does the
package own a table, or drive one.

> **primitives-go ships what every service is built from and no service is.**
> Four kinds of thing qualify: a provider behind an interface (`cache`, `email`,
> `messagequeue`, ...); a transport whose shape is decided by something other
> than the consumer's domain (a probe, a protocol, a middleware contract, a
> third party's payload); the database and schema tooling stores are built with
> (`database` and its subpackages, `filtering`); and the cross-cutting values
> both tiers have to agree on (`tenancy.Scope`, the `errors` sentinels,
> `clock`). Nothing in it owns a table.
>
> **platform-go ships what a product has**: a noun with a table, its lifecycle,
> its transport, its permissions and its privacy obligations. The test for a new
> package is whether an application with no users would still need it. If yes, it
> is a primitive.

The table below is this module's side of that sort. It is not the whole rule's
answer and is not meant to be: what the primitives are is
[primitives-go's README](https://github.com/primandproper/primitives-go#readme)
to list, and a copy of that list here would be a second answer with nothing
checking it.

| What it is                                     | Packages |
|------------------------------------------------|----------|
| a noun with a table, and what it owes          | `audit`, `authentication/oauth2clients`, `authentication/oauth2server/database`, `authentication/passwordreset`, `authentication/webauthn/database`, `authorization/database`, `billing`, `comments`, `cryptography/shredding`, `dataprivacy`, `entitlements`, `identity`, `issuereports`, `links`, `metering`, `notifications`, `operations`, `outbox`, `retention`, `saga`, `search/sync`, `sessions`, `settings`, `timers`, `uploads/registry`, `waitlists`, `webhooks`, `workqueue` |
| a domain flow over another domain's tables     | `authentication/signin` |
| the composition root that registers both tiers | `errormappers`, `service` |

The second row is the one the rule's own wording anticipates when it asks whether
a package owns a table *or drives one*. `authentication/signin` owns no schema
and never will: it is the order the engines and the directory are used in — read
the handle, compare the hash, check the status, ask for the code, mint the token
— and every row it touches is `identity`'s. It is still emphatically the domain
tier, because an application with no users has nobody to sign in, and because the
refusals it collapses are a product decision rather than a mechanism. A package
like it is the shape to expect as more domains arrive: the flows over the nouns,
after the nouns.

Nine of the paths above sit under a directory this module does not own the root
of, and each is a straddle the split left standing. Six are a primitive with a
store nested inside it — `authentication` hashes passwords and issues tokens in
primitives-go, and `authentication/passwordreset` owns a table of them;
`authentication/oauth2clients`, `authentication/oauth2server/database`,
`authentication/webauthn/database`, `authorization/database`,
`cryptography/shredding` and `uploads/registry` split the same way. `search/sync`
is the reindexing worker driven by the outbox, under two search indexes that are
primitives. The ninth is `authentication/signin`, which is neither: it is a
domain flow under a primitive's path, there because sign-in is what those engines
are for and a `signin` at the root would hide that.

The nested stores are self-contained, and Go is content with a parent directory
holding no `.go` files — `authentication/`, `authorization/`, `cryptography/`,
`search/` and `uploads/` are now exactly that here, and hold the engines in the
other module. What was not self-contained was the *configuration*: a `config`
subpackage that picked a store by dispatching on a provider string named every
package it might build from, so three of them named a table. The rule that
predicts it is worth stating once, because it is what any future straddle will be
measured against:

> **A config that takes a store as a parameter is clean; a config that builds one
> by dispatching on a provider string is stuck.** The provider string exists
> because a second implementation exists, so it belongs with the implementation
> that created the choice.

So `authorization/config`, `authentication/webauthn/config` and
`authentication/oauth2server/config` kept everything that needs no table and went
to primitives-go, and the provider string, the store's own config block and the
dispatch moved to a `config` subpackage beside the store, which stayed here:
`authorization/database/config`, `authentication/webauthn/database/config`,
`authentication/oauth2server/database/config`. The domain half embeds the
primitive half's `Config` with no `env` tag on the embed, so every environment
variable an operator sets resolves at the name it always did, and each package's
`doc.go` records the decision and the two alternatives that were refused.

`notifications/mobile` was the mirror and needed no split at all: it named
`notifications` only to spell a DI key, for a one-method interface it already
owned, so `notifications/config` registers that narrowing instead and the push
providers left with the other primitives.

None of this is enforced by prose. `internal/tiercheck` is the roster: every
package in the tree is named, checked against this table in both directions, and
a package ruled a primitive fails there, because a primitive is a package this
repository does not hold. The direction the split bought — primitives-go imports
platform-go from nowhere, ever — is checked on the other side, by primitives-go's
`internal/tierguard`, which needs no roster because the answer is the same for
every package in that module.

`service` is neither tier and is why the split did not split it: it is one walk
of one config that registers both modules, and a consumer of both sees the wiring
it saw before. `errormappers` is the small half of the same job — the one call
that tells the two transport registries what the domain tier's sentinels mean —
kept out of `service` so that a consumer wiring three packages by hand does not
import the config tree to make it.

### Transports

A component here that owns data ships a `Store` interface, a SQL implementation
of it, the DDL for whichever dialects the matrix below grants it, and a mock.
For most of them it stops there: the HTTP handlers over that store are not
missing, they are yours, and a library that shipped them would be versioning
your `/api/v1/users` on its own release cadence, in types your proto does not
have, under a scoping rule it guessed.

All three of those were properties of a module that also held the primitives,
and the split has answered each. The **cadence** is the domain tier's own now,
since nothing else rides on a release it is in — a fix to `retry` is a
primitives-go release a consumer takes without reading a migration note. The
**types** are shipped: primitives-go ships `filtering.proto` inside its
published module and a domain's `.proto` travels the same way from here —
generated into Go beside it, and into a consumer's Swift, TypeScript and Kotlin
from the same file. The **scope** is not guessed, because `tenancy.Scope` exists
and a domain transport binds it off the caller rather than off a request field.

So the line moves, one domain at a time, and `identity` is the first across it.
`identity/grpc` serves the directory: twenty-eight RPCs, the `.proto` they are
described by, a typed client, and the permissions each one wants. What it still
does not ship is the policy — who is calling is an interface the consumer's own
authentication interceptor satisfies, and what each method requires is a default
map a consumer composes into its own. That is the same bargain `identity` always
stated, one layer further out: a consumer keeps its policy and whatever columns
are genuinely its own, and does not keep a users table, the transaction-shaped
code around one, or the service and converters over that.

`authentication/signin` is the second across, and it crosses differently: it owns
no table at all. It is sign-in — the order `argon2`, `totp`, `tokens` and
`identity` are used in, which is the code every application writes over those
four and the code where their bugs live. The engines each do one thing and store
nothing; the directory stores what they produce and never calls them; nothing
joined them up. What it decides is the refusals, and it collapses four of them
into one sentinel on purpose, because telling an unknown handle from a wrong
password is telling an attacker which half of the guess was right. What it
refuses to decide is the rest: whether a second factor is mandatory, whether the
administrative door exists, how long a token lives and what it carries are four
options with four defaults. `authentication/signin/grpc` serves it, and is the
one surface in the module that reads its tenant off the connection rather than
off a caller — because a caller signing in has not become one yet.

Ten more still ship a store and no handlers, and each is to follow `identity`.
The transport is not uniform and neither is the subset of a store that crosses:

| package | verdict | transport | carved out, and why |
|---|---|---|---|
| `waitlists` | wire surface, full | gRPC | — |
| `comments` | wire surface, full | gRPC | the two bulk deletes — erasure machinery |
| `issuereports` | wire surface, full | gRPC | `DeleteReportsByReporter` — erasure machinery |
| `settings` | wire surface, full | gRPC | `DeleteValuesForSubject` — erasure machinery |
| `notifications` | wire surface, both halves | gRPC | `CreateNotification`, `ListDevicesByPrincipals`, `InvalidateDeviceToken` |
| `webhooks` | wire surface, management + history | gRPC | `Enqueue`, `EndpointsForEvent`, and the seven its store documents |
| `billing` | wire surface, read-biased | gRPC | the four status moves, whose caller is a processor callback already inside your transaction |
| `audit` | wire surface, read-only and scope-bound | gRPC | `Record`, and `Query.Scope` itself |
| `dataprivacy` | wire surface over the existing `Service` | HTTP | — |
| `uploads/registry` | binding, not a resource surface | HTTP | all seven store methods; what ships is the guarded serve |

Seven get nothing, and saying so is the point of this section rather than
leaving them unmentioned: `metering`, `saga`, `timers`, `workqueue`, `outbox`,
`retention` and `entitlements` are machinery. Their methods are called by a
worker on a timer, or by your own code inside your own transaction, which is the
same test the carve-outs above are made by. Owning a store is not what puts a
package on the list; having a caller who is somebody else is.

Two of the verdicts are not the house default, and each has a stated reason.
`uploads/registry` is a binding rather than a resource surface. Its own
documentation heads a section *"Why the row is the access control"* — whether
this caller may read this object is answered from the owner and the scope on the
row, not from the bucket — and then declines to act on it, because nothing in
that package opens, reads or removes an object. A metadata surface would ship
seven flat methods and leave you the guarded serve, which is the half that gets
written wrong: an unguessable key as the only protection a private document has.
`dataprivacy` is on HTTP because its flow already is. Progress is answered by
`operations/http` against `Request.OperationID` and the same event stream every
other long-running thing here uses, `Confirm` is reached by somebody clicking a
link in a mail the notifier sent, and the artifact arrives as a freshly minted,
expiring `DownloadURL`. A gRPC surface would put submit, confirm and cancel on
one protocol while the confirm click, the progress stream and the download all
lived on another.

The flows over those nouns are the other half of the same list, and sign-in is
the first of them. Passkeys, session management, password reset and email
verification are each their own addition over an engine this module already
ships, rather than a branch inside the password flow.

The line the primitives are held to went with them. It read: *a module ships a
transport for a primitive only where the shape of the request is decided by
something other than the consumer's domain* — a probe, a protocol, a middleware
contract, or a third party's payload — and the probes, the middleware and
`webhooks/inbound`'s receiver for a Stripe or GitHub callback are primitives-go's
to hold to it. What is left here is the second half of that sentence, and it is
the whole list.

<!-- readmegen:transports -->
| Transport                           | Kind             | Whose shape it is                                                                               |
|-------------------------------------|------------------|-------------------------------------------------------------------------------------------------|
| `sessions/http`                     | binding          | a signed cookie, whose security properties are ours                                             |
| `authentication/oauth2clients/grpc` | resource surface | an administered OAuth2 client registry — over `oauth2clients.Service` and `oauth2clients.Store` |
| `authentication/signin/grpc`        | resource surface | sign-in and the credentials a person changes about themselves — over `signin.Service`           |
| `identity/grpc`                     | resource surface | the four nouns and their lifecycle — over `identity.Service` and `identity.Store`               |
| `operations/http`                   | resource surface | poll, list, cancel, subscribe — over `Operation`                                                |
<!-- /readmegen:transports -->

One row is a binding rather than a surface. `sessions/http` binds a store to a
cookie, and a cookie's signing, encryption, `HttpOnly`, `Secure` and `SameSite`
are security decisions this module already made — there is no resource of yours
in it.

The other four are resource surfaces, and they get there by two routes.
`operations/http` is entirely this module's own resource: an `Operation`, its
two-tier progress and its state machine are types you did not define, and
polling one or subscribing to its server-sent events is the pattern's protocol
rather than your API. *Starting* an operation is yours, and is deliberately not
there. `identity/grpc`, `authentication/signin/grpc` and
`authentication/oauth2clients/grpc` are the other kind — a domain's own
transport, shipped under the rule above rather than as an exception to it, and
the first three of the thirteen to cross.

The table is not written by hand either. `internal/cmd/readmegen` emits it on
`make generate` from the `http` and `grpc` directories the tree ships, and
refuses to emit a row for one whose own `doc.go` does not name its kind and
whose shape it is standing in for. A package that grows handlers therefore
cannot reach `main` without somebody having said which side of the line they
fall on.

## SQL Dialect Support

`database` speaks Postgres, MySQL and SQLite, and so does almost every package
that stores anything through it. Three do not. They are Postgres-only by
decision rather than by omission, and this is where that decision is spoken —
once, before you choose packages, rather than package by package as each
constructor refuses at wiring time.

A ✓ means the package ships DDL for that dialect, and — for every package whose
statements have been ported onto the generated tier — executes a querier emitted
against it. Everything unticked returns `dialect.ErrUnsupported` at
construction, never a partial store or a migration that creates nothing.

<!-- readmegen:dialects -->
| Package                                | Postgres | MySQL | SQLite |
|----------------------------------------|----------|-------|--------|
| `audit`                                | ✓        | ✓     | ✓      |
| `authentication/oauth2clients`         | ✓        | ✓     | ✓      |
| `authentication/oauth2server/database` | ✓        | ✓     | ✓      |
| `authentication/passwordreset`         | ✓        | ✓     | ✓      |
| `authentication/webauthn/database`     | ✓        | ✓     | ✓      |
| `authorization/database`               | ✓        | ✓     | ✓      |
| `billing`                              | ✓        | ✓     | ✓      |
| `comments`                             | ✓        | ✓     | ✓      |
| `cryptography/shredding`               | ✓        | ✓     | ✓      |
| `dataprivacy`                          | ✓        | ✓     | ✓      |
| `identity`                             | ✓        | ✓     | ✓      |
| `issuereports`                         | ✓        | ✓     | ✓      |
| `links/database`                       | ✓        | ✓     | ✓      |
| `metering`                             | ✓        | ✓     | ✓      |
| `notifications`                        | ✓        | ✓     | ✓      |
| `operations`                           | ✓        | —     | —      |
| `outbox`                               | ✓        | ✓     | ✓      |
| `saga`                                 | ✓        | ✓     | ✓      |
| `sessions/database`                    | ✓        | ✓     | ✓      |
| `settings`                             | ✓        | ✓     | ✓      |
| `timers`                               | ✓        | —     | —      |
| `uploads/registry`                     | ✓        | ✓     | ✓      |
| `waitlists`                            | ✓        | ✓     | ✓      |
| `webhooks`                             | ✓        | ✓     | ✓      |
| `workqueue`                            | ✓        | —     | —      |
<!-- /readmegen:dialects -->

### Why the three narrow

One reason, arrived at from three directions, and it is a claim rather than a
translation. The claim is a single statement that selects due rows, locks them
with `SKIP LOCKED`, increments attempts, extends the lease and hands the keys
back with `RETURNING`. MySQL 8.0 has `SKIP LOCKED` and CTEs but no `RETURNING`,
so the same claim there is a `SELECT … FOR UPDATE SKIP LOCKED` plus a separate
`UPDATE` inside a transaction held across both round trips — a different
concurrency shape with a different failure model, which is a second
implementation rather than a dialect switch. SQLite is a harder no:
single-writer, with no row-level locking to skip.

Each narrowed package states where it stands in that, in its own `doc.go`, and
these lines are that statement:

<!-- readmegen:narrowings -->
- `operations` — runs on `workqueue`, so its roster is `workqueue`'s.
- `timers` — claims a due timer in the one statement, and would owe the same split anywhere else.
- `workqueue` — the claim is the package: `SKIP LOCKED` to take due rows and `RETURNING` to hand the keys back, in one round trip.
<!-- /readmegen:narrowings -->

Widening any of them is a decision about that claim, not about a missing
translation — the package docs carry the long form. Nothing forecloses it: the
shape to reach for is the package as the interface with a provider subpackage
beneath it, the way primitives-go's `cache` and `cache/redis` sit.

### Narrowings that are not rows

Some dialect-dependence is a capability inside a package that serves all three,
and a row would misreport it either way:

- **`outbox`** stores and relays on all three. Its `LISTEN`/`NOTIFY` wakeup is
  Postgres-only and reported as `outbox.ErrNotifyUnsupported` if configured
  elsewhere; without it a relay polls, which is later rather than wrong. Its
  `SKIP LOCKED` claim mode degrades to a lease on SQLite.
- **`retention`** sweeps all three, and ships no DDL: the table, the timestamp
  column and the batch key arrive from a `Policy` written at run time, so there
  is no schema of this module's to render for a dialect.

A primitive that names a dialect is the same case and is primitives-go's to
report: `distributedlock/postgres` and `search/vector/pgvector` are named
providers beside `memory`, `redis` and `qdrant`, chosen by config the way
`cache/redis` is, and picking one is picking Postgres, which is what its name
says.

The matrix above is not written by hand. `internal/cmd/readmegen` emits it on
`make generate` from the DDL each package ships, checks that against the
dialects its generated querier was emitted for, and refuses to emit a short row
for a package whose `doc.go` does not say why it is short. A package that gains
or loses a dialect therefore changes this file on the next generate, and the
generated-files workflow reds until that change is committed.

## Development

```bash
make setup          # Install dev tools and download deps
make format         # Format all Go code (imports, field/tag alignment, gofmt)
make lint           # Run golangci-lint (Docker) + shellcheck
make test           # Run tests (race detector, shuffle, failfast)
make build          # Build all packages
make generate       # Regenerate moq mocks after changing a mocked interface
make bench          # Run benchmarks
```

Formatting runs locally with `gci`, `goimports`, `betteralign`, `tagalign`, and `gofmt`. Linting runs in Docker against the `golangci/golangci-lint` image (42+ linters, golangci-lint v2 format).

### Testing conventions

- **`stretchr/testify` is banned** (`assert`, `require`, and `mock`), enforced by `depguard`. Use [`shoenig/test`](https://github.com/shoenig/test) for assertions (`test` for non-fatal, `must` for fatal) and [`matryer/moq`](https://github.com/matryer/moq) for mocks.
- Tests run in parallel by default and use subtests throughout.
- Container-backed tests use `testcontainers-go`, live in-package (typically `containers_test.go`), and gate on `RUN_CONTAINER_TESTS=true`.
- `make test` runs `CGO_ENABLED=1 go test -shuffle=on -race -vet=all -failfast ./...` across every package. `.scripts/test.sh false` runs the suite without container tests.

## Contributing

Because `main` is a development channel and only tagged releases are supported, changes land on `main` freely and are stabilized before release. Follow the existing package layout (the store, its `config` subpackage, its `migrations`, its rendered query corpus and its mock), match the surrounding code, and keep `make format lint test` green. A package that turns out to be a primitive belongs in [`primitives-go`](https://github.com/primandproper/primitives-go) instead — [Primitives and Domains](#primitives-and-domains) is the rule, and `internal/tiercheck` fails a roster entry that claims one.

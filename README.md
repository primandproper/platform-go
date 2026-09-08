# platform-go

[![Go Reference](https://pkg.go.dev/badge/github.com/primandproper/platform-go/v14.svg)](https://pkg.go.dev/github.com/primandproper/platform-go/v14) [![codecov](https://codecov.io/github/primandproper/platform-go/graph/badge.svg?token=69RLLWLJ39)](https://codecov.io/github/primandproper/platform-go)

A Go library providing infrastructure abstractions for cloud-native services. Each package defines a stable interface with one or more provider implementations, selected at runtime via config. Layers that touch the network — HTTP, gRPC, database, messaging — instrument with OpenTelemetry.

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

**Interface + implementations.** Every major concern is defined as an interface (e.g., `cache.Cache[T]`, `logging.Logger`, `secrets.SecretSource`), with provider implementations in subpackages. Swap implementations via config without touching call sites. Most packages ship a `noop` implementation for tests and for cleanly disabling a concern.

**Config structs.** Each package has a `config` subpackage with `env:`-tagged structs and `ValidateWithContext()` (via `go-ozzo/ozzo-validation`). Configuration is the seam that selects an implementation. Most, but not all, also have `EnsureDefaults()` — packages whose defaults are expressible as `envDefault:` tags use those instead.

Selecting an implementation is deliberate: an unrecognized provider name returns `errors.ErrUnknownProvider` rather than a working-looking noop, because a typo that silently discards every message or never limits a request is a production incident that looks like a healthy process. Where a noop is genuinely wanted it has to be asked for by name.

**OpenTelemetry throughout.** HTTP, gRPC, database, and messaging layers emit traces and metrics. Observability primitives (logging, tracing, metrics, profiling) live under `observability/`.

**Error handling.** Uses [`cockroachdb/errors`](https://github.com/cockroachdb/errors) for rich, wrapped error context. Platform-level sentinel errors live in `errors/`, conventionally imported as `platformerrors`. Transport mappings live in `errors/http` and `errors/grpc`, which map the primitives — `database`, `circuitbreaking`, `ratelimiting`, `idempotency`, `requestsigning`, the search indexes — and import those packages, so nothing in them may import back. Everything built on top maps itself: `dataprivacy`, `identity`, `links`, `operations` and `sessions` each export an `HTTPMapper` and a `GRPCMapper`, and the composition root registers the five in one call — `errormappers.Register()`, which `service.Register` makes for a service built from a `service.Config` and a service assembled by hand makes itself. `operations/http.New` is the single exception, registering its own HTTP mapper because it is the only surface here that both answers through `errors/http` and belongs to a package on that list. `internal/sentinelmatrix` checks that every exported sentinel in those five has a decision recorded and that it still holds on both transports.

## Package Catalog

Implementations are listed in parentheses; most concerns also provide a `noop`. Where an implementation is a SQL dialect, [SQL Dialect Support](#sql-dialect-support) is the full matrix and the reasons behind the three exceptions.

### Data & storage
| Package    | Purpose                              | Implementations                       |
|------------|--------------------------------------|---------------------------------------|
| `database` | SQL access + instrumentation         | postgres, mysql, sqlite               |
| `cache`    | Generic key/value cache (`Cache[T]`) | redis, memory                         |
| `uploads`  | Blob/object storage & image handling | objectstorage (S3-compatible), images |
| `files`    | Filesystem & streaming helpers       | —                                     |
| `secrets`  | Secret sourcing (+ caching/rotation) | env, gcp, ssm, kubernetes                |

### Messaging & events
| Package         | Purpose                    | Implementations                                   |
|-----------------|----------------------------|---------------------------------------------------|
| `messagequeue`  | Publish/subscribe & queues | kafka, pubsub, redis, sqs                         |
| `outbox`        | Transactional outbox       | postgres, mysql, sqlite                           |
| `eventstream`   | Server push to clients     | sse, websocket                                    |
| `notifications` | User notifications         | async, mobile                                     |
| `jobs`          | Queue workers & periodic jobs | —                                              |
| `email`         | Transactional email        | mailgun, mailjet, postmark, resend, sendgrid, ses |

### Web & transport
| Package           | Purpose                   | Implementations |
|-------------------|---------------------------|-----------------|
| `server`          | Service servers           | grpc, http      |
| `routing`         | HTTP router abstraction   | chi, stdlib, httprouter, gin |
| `httpclient`      | Instrumented HTTP client  | —               |
| `cookies`         | Cookie management         | —               |
| `encoding`        | Content encoding/decoding | —               |
| `compression`     | Payload compression       | —               |
| `ratelimiting`    | Request rate limiting     | redis           |
| `circuitbreaking` | Circuit breaker           | —               |
| `retry`           | Retry with backoff        | —               |
| `idempotency`     | At-most-once effect for retried requests | http, grpc (server + client) |

### Observability & operations
| Package         | Purpose                              | Implementations                                    |
|-----------------|--------------------------------------|----------------------------------------------------|
| `observability` | Logging, tracing, metrics, profiling | logging (slog, zap, zerolog); OTel tracing/metrics |
| `healthcheck`   | Health/readiness checks              | —                                                  |
| `version`       | Build/version metadata               | —                                                  |
| `metering`      | Durable usage metering & quotas      | postgres, mysql, sqlite                            |
| `webhooks`      | Outbound webhook delivery            | postgres, mysql, sqlite                            |
| `webhooks/inbound` | Inbound webhook receipt: verify, publish, ack | stripe, github, generic HMAC          |
| `clock`         | Injectable time                      | —                                                  |
| `config`        | Config loading & env parsing         | —                                                  |

### Auth & security
| Package          | Purpose                             | Implementations                |
|------------------|-------------------------------------|--------------------------------|
| `authentication` | Password hashing, TOTP, tokens      | argon2, totp, tokens           |
| `authentication/webauthn` | Passkey registration & login, with ceremony state that outlives one replica | database, cache |
| `authentication/passwordreset` | Password reset tokens: digest at rest, single use enforced by the store | postgres, mysql, sqlite |
| `sessions`       | Server-side sessions over cookies   | cache, database (+ http)       |
| `authorization`  | Role/permission policy, enforcement | static (default), database     |
| `links`          | Signed, expiring, single-use action links | postgres, mysql, sqlite        |
| `audit`          | Tamper-evident audit log            | postgres, mysql, sqlite        |
| `cryptography`   | Cryptographic primitives            | encryption (aes, kms), hashing |
| `cryptography/requestsigning` | HMAC request signing & verification | v1                             |
| `cryptography/shredding` | Per-subject data keys that can be destroyed | postgres, mysql, sqlite |
| `random`         | Secure randomness                   | —                              |
| `identifiers`    | ID generation                       | —                              |
| `dataprivacy`    | Subject access & erasure requests   | postgres, mysql, sqlite        |
| `retention`      | Policy-driven expiry deletion       | postgres, mysql, sqlite        |

### AI, ML & product
| Package        | Purpose                      | Implementations               |
|----------------|------------------------------|-------------------------------|
| `llm`          | Large language model clients | anthropic, openai             |
| `embeddings`   | Embedding generation         | cohere, ollama, openai        |
| `search`       | Vector / text search         | vector, text                  |
| `analytics`    | Product analytics            | posthog, segment, multisource |
| `featureflags` | Feature flagging             | launchdarkly, posthog         |

### Domain & coordination
| Package           | Purpose                    | Implementations         |
|-------------------|----------------------------|-------------------------|
| `capitalism`      | Payment provider adapters  | stripe, revenuecat      |
| `billing`         | What a deployment sells, and what its customers paid: catalog, subscriptions, purchases, ledger | postgres, mysql, sqlite |
| `entitlements`    | Feature access & remaining quota | —                 |
| `settings`        | Per-user and per-account runtime settings: admin-defined definitions, per-subject values | postgres, mysql, sqlite |
| `issuereports`    | User-submitted issue reports with a triage lifecycle | postgres, mysql, sqlite |
| `comments`        | Threaded comments on consumer-declared targets | postgres, mysql, sqlite |
| `saga`            | Linear durable sagas with compensations | postgres, mysql, sqlite |
| `distributedlock` | Distributed locking        | memory, postgres, redis |
| `workqueue`       | Leased work queue (`SKIP LOCKED` claim/complete/expire) | postgres |
| `timers`          | Durable one-shot scheduling (run once at time T, fleet-wide) | postgres |
| `operations`      | Long-running operations with durable state, two-tier progress, and streamed updates | postgres |
| `filtering`       | Query filters / pagination | —                       |
| `qrcodes`         | QR code generation         | —                       |
| `waitlists`       | Pre-launch waitlists: signup lifecycle, and an unsubscribe that outlives the address | postgres, mysql, sqlite |
| `eventcapture`    | Recording domain events    | jsonl                   |

### Utilities
`errors`, `pointer`, `numbers`, `bitmask`, `charset`, `reflection`, `panicking`, `testutils`, `fake`.

## Primitives and Domains

There are two kinds of package here, and they are separating: the primitives
leave for `primitives-go`, and what stays is the domain tier. The rule that
sorts them is the one to check a new package against before writing it, and it
is one property — does the package own a table, or drive one.

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

Both tiers are still in this module — the move has not landed — so the tier
column below is where a package is going rather than where it can be imported
from today. The rule is what a new package is measured against either way.

| Tier            | What it is                                                | Packages |
|-----------------|-----------------------------------------------------------|----------|
| `primitives-go` | a provider behind an interface                            | `analytics`, `authentication`, `authorization`, `cache`, `capitalism`, `cryptography`, `distributedlock`, `email`, `embeddings`, `eventcapture`, `eventstream`, `featureflags`, `llm`, `messagequeue`, `notifications/mobile`, `ratelimiting`, `search`, `secrets`, `uploads` |
| `primitives-go` | a transport whose shape is not the consumer's             | `compression`, `cookies`, `encoding`, `healthcheck`, `httpclient`, `idempotency`, `routing`, `server`, `webhooks/inbound` |
| `primitives-go` | the database and schema tooling stores are built with     | `database`, `filtering` |
| `primitives-go` | the cross-cutting values and utilities both tiers build on | `batching`, `bitmask`, `charset`, `circuitbreaking`, `clock`, `config`, `errors`, `fake`, `files`, `identifiers`, `jobs`, `numbers`, `observability`, `panicking`, `pointer`, `qrcodes`, `random`, `reflection`, `retry`, `tenancy`, `testutils`, `version` |
| `platform-go`   | a noun with a table, and what it owes                     | `audit`, `authentication/oauth2clients`, `authentication/oauth2server/database`, `authentication/passwordreset`, `authentication/webauthn/database`, `authorization/database`, `billing`, `comments`, `cryptography/shredding`, `dataprivacy`, `entitlements`, `identity`, `issuereports`, `links`, `metering`, `notifications`, `operations`, `outbox`, `retention`, `saga`, `search/sync`, `sessions`, `settings`, `timers`, `uploads/registry`, `waitlists`, `webhooks`, `workqueue` |
| `platform-go`   | a domain flow over another domain's tables                 | `authentication/signin` |
| `platform-go`   | the composition root that registers both tiers            | `errormappers`, `service` |

The fifth row is the one the rule's own wording anticipates when it asks whether
a package owns a table *or drives one*. `authentication/signin` owns no schema
and never will: it is the order the engines and the directory are used in — read
the handle, compare the hash, check the status, ask for the code, mint the token
— and every row it touches is `identity`'s. It is still emphatically the domain
tier, because an application with no users has nobody to sign in, and because the
refusals it collapses are a product decision rather than a mechanism. A package
like it is the shape to expect as more domains cross: the flows over the nouns,
after the nouns.

A package with a path of its own on the other side is a package straddling the
line, and today seven do. Four are a primitive with a store nested inside it — `authentication` hashes
passwords and issues tokens, and `authentication/passwordreset` owns a table of
them; `authorization`, `cryptography` and `uploads` split the same way. Two are
the mirror: `notifications` owns the inbox and `notifications/mobile` is a push
provider behind an interface, and `search` is text and vector search while
`search/sync` is a reindexing worker driven by the outbox. The seventh is
`authentication/signin`, which is neither: it is a domain flow under a primitive's
path, there because sign-in is what those engines are for and a `signin` at the
root would hide that.

The nested stores themselves are self-contained, and Go is content with a parent
directory holding no `.go` files — `cryptography/` is already exactly that. What
was not self-contained was the *configuration*: a `config` subpackage that picked
a store by dispatching on a provider string named every package it might build
from, so three of them named a table. The rule that predicts it is worth stating
once, because it is what any future straddle will be measured against:

> **A config that takes a store as a parameter is clean; a config that builds one
> by dispatching on a provider string is stuck.** The provider string exists
> because a second implementation exists, so it belongs with the implementation
> that created the choice.

So `authorization/config`, `authentication/webauthn/config` and
`authentication/oauth2server/config` keep everything that needs no table, and the
provider string, the store's own config block and the dispatch moved to a
`config` subpackage beside the store: `authorization/database/config`,
`authentication/webauthn/database/config`,
`authentication/oauth2server/database/config`. The domain half embeds the
primitive half's `Config` with no `env` tag on the embed, so every environment
variable an operator sets resolves at the name it always did, and each package's
`doc.go` records the decision and the two alternatives that were refused.

`notifications/mobile` was the mirror again and needed no split at all: it named
`notifications` only to spell a DI key, for a one-method interface it already
owned, so `notifications/config` registers that narrowing instead.

None of this is enforced by prose. `internal/tiercheck` is the roster: every
package in the tree is named with its tier, checked against this table in both
directions, and a test walks every import — test files included, since a
primitives-go test is compiled by primitives-go — and fails on a primitive that
names a domain.

`service` is neither tier and is why the split does not split it: it is one walk
of one config that registers both, and a consumer of both sees the wiring it
sees today. `errormappers` is the small half of the same job that does sit on
this side — the one call that tells the two transport registries what the domain
tier's sentinels mean — kept out of `service` so that a consumer wiring three
packages by hand does not import the config tree of seventy to make it.

### Transports

A component here that owns data ships a `Store` interface, a SQL implementation
of it, the DDL for whichever dialects the matrix below grants it, and a mock.
For most of them it stops there: the HTTP handlers over that store are not
missing, they are yours, and a library that shipped them would be versioning
your `/api/v1/users` on its own release cadence, in types your proto does not
have, under a scoping rule it guessed.

All three of those were properties of a module that also held the primitives,
and the split answers each. The **cadence** becomes the domain tier's own, since
nothing else rides on a release it is in. The **types** are shipped: this module
already ships `filtering.proto` inside the published module, and a domain's
`.proto` travels the same way — generated into Go here, and into a consumer's
Swift, TypeScript and Kotlin from the same file. The **scope** is not guessed,
because `tenancy.Scope` exists and a domain transport binds it off the caller
rather than off a request field.

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

`webhooks` endpoint management, `billing`, `settings`, `notifications`,
`metering`, `audit`, `dataprivacy`, `saga`, `timers` and `workqueue` still ship
a store and no handlers. Each is to follow `identity`, which is the
pattern-setting one and the reason the rest waited; none has been filed yet.

The flows over those nouns are the other half of the same list, and sign-in is
the first of them. Passkeys, session management, password reset and email
verification are each their own addition over an engine this module already
ships, rather than a branch inside the password flow.

For the primitives the original line is unchanged, and it is this:

> **This module ships a transport for a primitive only where the shape of the
> request is decided by something other than the consumer's domain** — a probe,
> a protocol, a middleware contract, or a third party's payload. A domain ships
> its own transport, and keeps the policy out of it.

Everything below is on the far side of that line, and it is the whole list.

<!-- readmegen:transports -->
| Transport                           | Kind             | Whose shape it is                                                                               |
|-------------------------------------|------------------|-------------------------------------------------------------------------------------------------|
| `server/http`                       | server           | the process: bind, serve, drain, and its own probes                                             |
| `server/grpc`                       | server           | the same, for gRPC                                                                              |
| `errors/http`                       | mapping          | a sentinel to a status code, and back                                                           |
| `errors/grpc`                       | mapping          | a sentinel to a gRPC code, and back                                                             |
| `filtering/grpc`                    | wire conversion  | `QueryFilter` and `Pagination` to their generated messages                                      |
| `authorization/http`                | middleware       | a route's declared requirement, checked before it runs                                          |
| `authorization/grpc`                | middleware       | the same, as interceptors                                                                       |
| `cryptography/requestsigning/http`  | middleware       | a signature verified before the handler runs                                                    |
| `idempotency/http`                  | middleware       | the `Idempotency-Key` header, both sides of the wire                                            |
| `idempotency/grpc`                  | middleware       | the same, over metadata                                                                         |
| `ratelimiting/http`                 | middleware       | a token per request, 429 when there is none                                                     |
| `ratelimiting/grpc`                 | middleware       | the same, as interceptors                                                                       |
| `sessions/http`                     | binding          | a signed cookie, whose security properties are ours                                             |
| `authentication/oauth2clients/grpc` | resource surface | an administered OAuth2 client registry — over `oauth2clients.Service` and `oauth2clients.Store` |
| `authentication/signin/grpc`        | resource surface | sign-in and the credentials a person changes about themselves — over `signin.Service`           |
| `identity/grpc`                     | resource surface | the four nouns and their lifecycle — over `identity.Service` and `identity.Store`               |
| `operations/http`                   | resource surface | poll, list, cancel, subscribe — over `Operation`                                                |
<!-- /readmegen:transports -->

The middleware rows carry nothing domain-shaped: they read a header or a claim
and let the request through or refuse it, and the handler behind them is still
yours. `sessions/http` binds a store to a cookie, and a cookie's signing,
encryption, `HttpOnly`, `Secure` and `SameSite` are security decisions this
module already made — there is no resource of yours in it.

Two rows are resource surfaces, and they get there by different routes.
`operations/http` is entirely this module's own resource: an `Operation`, its
two-tier progress and its state machine are types you did not define, and
polling one or subscribing to its server-sent events is the pattern's protocol
rather than your API. *Starting* an operation is yours, and is deliberately not
there. `identity/grpc` is the other kind — a domain's own transport, shipped
under the rule above rather than as an exception to it, and the first of the ten
listed above that will follow it.

One transport sits outside the table because it is not an `http` subpackage:
`webhooks/inbound` ships a `Receiver` that is an `http.Handler`, for the same
reason — a Stripe or GitHub callback's shape is decided by Stripe or GitHub, and
you have no say in it either.

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
beneath it, the way `cache` and `cache/redis` sit.

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
- **`distributedlock/postgres`** and **`search/vector/pgvector`** are named
  providers beside `memory`, `redis` and `qdrant`, chosen by config the way
  `cache/redis` is. Picking one is picking Postgres, which is what its name
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

Because `main` is a development channel and only tagged releases are supported, changes land on `main` freely and are stabilized before release. Follow the existing package layout (interface + config subpackage + provider implementations + `noop`), match the surrounding code, and keep `make format lint test` green.

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Go library/framework (`github.com/primandproper/platform-go/v14`) providing infrastructure abstractions for cloud-native services: database, caching, messaging, observability, secrets, uploads, email, payments, and more. Single module, ~40 packages, Go 1.26.

## Common Commands

```bash
make format         # Format all Go code (imports, field alignment, tag alignment, gofmt)
make lint           # Run golangci-lint (Docker) + shellcheck
make format lint    # Typical workflow: format then lint
make test           # Run tests (race detector, shuffle, failfast)
make build          # Build all packages
make generate       # Regenerate moq mocks after changing any mocked interface
make proto format   # Regenerate the Go bindings for the .proto files this module ships
make unison format  # Regenerate the sqlc-gen-unison packages (identity's identitydb) + schema files
make setup          # Install dev tools + download deps
```

Run a single test:
```bash
go test -run TestFunctionName ./package/path/...
```

Run tests for a single package:
```bash
go test -race ./cache/...
```

Linting runs in Docker (`golangci/golangci-lint` image). Formatting runs locally with `gci`, `goimports`, `betteralign`, `tagalign`, and `gofmt`.

**This module does not vendor its dependencies.** `go` resolves through the module
cache, and there is deliberately no `vendor/` directory or `make revendor` — a
vendor directory does not track `go.mod`, and once one exists every `go` command in
the tree silently prefers it, so a dependency bump or a branch switch leaves the
next command failing with go's "inconsistent vendoring" wall, printed by whatever
tool happened to run next. The containerised linter gets a bind-mounted module and
build cache (`GO_CACHE`) instead, which is what `vendor/` was really buying.

(`reflection/ast` reads a *consumer's* `vendor/` directory when one is present.
That is a feature of the library and unrelated to how this repo builds itself.)

## Import Ordering

Import ordering uses `gci` with four sections, separated by blank lines:

1. Standard library
2. `github.com/primandproper/platform-go/v14` (this module)
2. `github.com/primandproper` (org-level packages)
4. Everything else (third-party)

The Makefile `THIS` variable must be the full module path (`github.com/primandproper/platform-go/v14`). `format_imports.sh` derives the org prefix from it by stripping any trailing major-version suffix (e.g. `/v2`) and then taking `dirname`, yielding `github.com/primandproper`. If `THIS` is too short, the org prefix collapses toward `github.com`, creating a spurious `prefix(github.com)` gci section.

## Architecture Patterns

**Two tiers, and a new package belongs to one of them.** Check a proposed package against the rule before writing it: **primitives-go ships what every service is built from and no service is** — a provider behind an interface, a transport whose shape is decided by something other than the consumer's domain (a probe, a protocol, a middleware contract, a third party's payload), the database and schema tooling stores are built with, and the cross-cutting values both tiers agree on (`tenancy.Scope`, the `errors` sentinels, `clock`). Nothing in it owns a table. **platform-go ships what a product has**: a noun with a table, its lifecycle, its transport, its permissions and its privacy obligations. The test is whether an application with no users would still need it — if yes, it is a primitive.

Both tiers are in this module today and the primitives are leaving for `primitives-go`, so the answer decides which repository a package is written for rather than which import path it gets right now. The README's "Primitives and Domains" section is where the rule and the current sort are stated. A new top-level package lands with a row there **and a row in `internal/tiercheck`'s roster**, and the two are checked against each other — the row is the decision, not the paperwork.

`internal/tiercheck` is also what enforces the direction: a primitives-tier package may not import a domain-tier one, test files included, because a primitives-go test is compiled by primitives-go. A nested package inherits its parent's tier by longest-prefix match, so a new subpackage is classified by construction; an entry is needed only where a path departs from what its parent already said.

**A config that builds a store by dispatching on a provider string is stuck on the domain side.** That is where the boundary was actually crossed — not by the nested stores, which are self-contained. The provider string exists because a second implementation exists, so it belongs with the implementation that created the choice: a config that *takes* a store as a parameter crosses nothing, and one that *builds* one names every package it might build from. `authorization`, `authentication/webauthn` and `authentication/oauth2server` are the worked examples, each split into a primitive `config` and a `database/config` beside the store, the second embedding the first's `Config` with no `env` tag on the embed, so a consumer's environment variables resolve unchanged (`caarlos0/env` reads through an anonymous embedded struct and still composes a nested `envPrefix`; YAML needs `yaml:",inline"`, JSON and TOML promote on their own). Each of the six packages' `doc.go` carries the ruling.

**Interface + multi-implementation:** Most packages define an interface with multiple implementations selected by config. Examples: `cache.Cache[T]` (Redis, memory), `logging.Logger` (slog, zap, zerolog), `secrets.SecretSource` (env, GCP, AWS SSM), `uploads` (S3, GCS, filesystem).

**Config structs:** Each major package has a `config` subpackage using `env:` struct tags and `ValidateWithContext()` via `go-ozzo/ozzo-validation`. Most, but not all, also have `EnsureDefaults()` — packages whose defaults are expressible as `envDefault:` tags use those instead.

Constructors call the validation their config defines, and apply defaults *before* validating: an unset field that has a documented default is not a validation failure, and validating first turns the common case into one. Selecting an implementation is deliberate — an unrecognized provider name returns `platformerrors.ErrUnknownProvider` rather than a working-looking noop. Where a noop is genuinely wanted it has to be named.

**OpenTelemetry throughout:** Database, HTTP, gRPC, and messaging all instrument with OTel for traces, metrics, and logs.

**Tenancy is a column, not a convention.** A component that stores consumer data stores it for somebody, and that somebody is a `tenancy.Scope` — an opaque owner identifier, deliberately not a `string`, so a scopeless call fails to compile rather than matching everything. `tenancy.Global()` is the scope of data belonging to no tenant; it matches only itself and is stored as the empty identifier, so a single-tenant application passes it everywhere and behaves exactly as it did before — delivered by the store binding it on every write, not by the column tolerating a write that omitted it.

Three obligations, and the third is the one that gets skipped. Scope in the column (`TEXT NOT NULL`, and deliberately no `DEFAULT ''` where the rest of the module's text columns have one — the empty string is `Global()` rather than "unset", so a default hands the global scope to a write that forgot to name one; never encoded into another column's value — a key like `"<accountID>:<eventType>"` scopes by construction and cannot be indexed, filtered, or enumerated as the two facts it is). Scope in the query, binding the `Scope` itself rather than a string derived from it, so an unset scope is a driver error instead of a wider result set. And **no read path that omits it** — not "a scoped variant exists": the caller who reaches for the unscoped one is the caller who has not thought about tenancy. A component's own machinery is the only exception, and it is narrow — a delivery worker draining a queue across every tenant is servicing itself, not answering a consumer read, and those methods say so in their documentation. `webhooks` is the worked example.

**A store write takes a `Tx`; a store read takes an executor.** Every exported store write reads `(ctx context.Context, tx database.Tx, scope tenancy.Scope, ...)`, and every exported store read reads `(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, ...)`. A `Tx` is producible only by `database.RunInTransaction`, so the write's signature is a compile-time claim that the caller is already inside a transaction — which is the whole point, because a consumer's write almost never travels alone. An audit entry, an outbox event and the row itself are one fact; a store that opens its own transaction is a store whose companions land in a second one, and a refused audit entry leaves a committed row with no provenance. The read takes the *wider* type deliberately: `Tx` satisfies `SQLQueryExecutor`, so one read serves both a caller holding `Client.Reader()` and a caller inside a transaction, and the second sees that transaction's own uncommitted writes. A read narrowed to `Reader()` would be reading a database that does not yet contain the row its caller just wrote.

The scope is an argument even where the write takes a whole entity that already carries one. `comments.Store.CreateComment` reads it off the argument and not off `Comment.Scope`, because a scope derived from a field is exactly the derivation the column rule exists to rule out — it makes "which tenant is this write for" answerable only by reading a struct the caller assembled somewhere else. An entity whose scope disagrees with the argument is refused rather than corrected (`comments.ErrScopeMismatch` is the worked example) and one that names none adopts it, which is the same reading `comments` already takes of a reply that names no target.

One way in, and it is `Client.WithTransaction`. A caller with nothing to join writes `client.WithTransaction(ctx, func(tx database.Tx) error { ... })`. There is deliberately no `database.Atomic(ctx, client, fn)` free function — it would be that method with its receiver moved into an argument, a second exported name for one behavior — and no per-store `WithTransaction` wrapper; `database.RunInTransaction` is the engine beneath the method, not application surface, and the rejection is recorded on `Client.WithTransaction` so it is not re-proposed a store at a time. What a store's constructor still takes a `database.Client` for is therefore per store rather than uniform: not for its writes, which are handed a `Tx`, but for `Reader()` if its reads are also callable outside a transaction, for `Dialect()` if it emits SQL, and for `CurrentTime()` if it stamps rows. A store with none of those obligations takes no `Client` at all. `database/convention_test.go` is the worked example, and pins the read-your-own-writes property the read shape exists for.

**A component's own machinery takes neither.** The rule above governs what a *consumer* calls; a worker draining a queue on a timer is the component servicing itself, and its writes take no executor at all — they run on the handle the store was built with, which is what those stores still take a `database.Client` for. `metering`'s flush protocol and `webhooks`' delivery queue are the two worked examples and were decided together: the correctness of both is that a claim commits *before* the outside world is touched, so a caller supplying a transaction would be choosing when that commit happens, which is the one thing neither protocol can let them choose, and a lease held open for the length of somebody else's transaction is work nobody else may claim and this worker may never do. It is the same carve-out tenancy already makes for machinery, and it is the same narrowness: the exception is a worker on a timer, not any method a component finds convenient to keep to itself. `webhooks.Store` names each of its seven and says why on the method.

**Error handling:** Uses `cockroachdb/errors` for rich error context. Platform-level sentinels are defined in `errors/`, conventionally imported as `platformerrors`.

Transport mappings split by tier. `errors/http` and `errors/grpc` map the primitives — `database`, `circuitbreaking`, `ratelimiting`, `idempotency`, `requestsigning`, the two search indexes, the `platformerrors` sentinels — and import those packages, so nothing in them may import `errors/http` or `errors/grpc` back. Everything built on top maps itself: `dataprivacy`, `identity`, `links`, `operations` and `sessions` each export an `HTTPMapper` and a `GRPCMapper` beside their sentinels and import the two transport packages. A sentinel whose own wording is meant for the person reading it goes to `grpcerrors.RegisterClientSafeSentinels` as well, or gRPC sends the code's name instead.

**The domain tier registers at the composition root, in one call.** `errormappers.Register()` installs all five pairs plus `links.ClientSafeSentinels`; `service.Register` calls it for a service built from a `service.Config` and a service assembled by hand calls it itself, next to the mappers it declares for its own sentinels. It lives outside `service` so that a consumer wiring three packages by hand does not import the whole config tree to get it, and it registers unconditionally — an unused mapper is one comparison against a sentinel the process cannot produce, and the expensive direction to be wrong in is a 500 for an error somebody took the trouble to give a status. No `init()` — a mapper that installs itself by being linked in is a side effect a consumer cannot opt out of. `operations/http.New` is the single exception, registering its own HTTP mapper because it is the only surface in the module that both answers through `errors/http` and belongs to a package on that list; `dataprivacy` and `links` ship no transport, `sessions/http` never writes an error response, and `identity/grpc` answers through `errors/grpc` rather than `errors/http`. `identity/grpc` deliberately does not follow the exception onto the gRPC side: one door stays one door, and `NewServer`'s documentation names the call a consumer owes instead.

`internal/sentinelmatrix` is the roster: every exported `Err` in those five packages is recorded as `mapped`, `platform` (it wraps a platform sentinel) or `unhandled` (a 500 is the honest answer), checked against the packages' own source in both directions and against what the mappers actually do. A sentinel added later is in none of the three and fails there rather than as `codes.Unknown` in a consumer.

**Options vs. config seams:** Constructors take `logger`/`tracerProvider`/`metricsProvider` as `WithX` options, never positionally — the `config` subpackages included. A config subpackage constructor reads `ctx, cfg, deps..., opts ...Option`, where `deps` are the things it genuinely cannot build (a `database.Client`, an `*http.Client`, a handler) and everything optional is an option.

Each config subpackage declares its own `Option` type, mirroring the leaf packages, with `WithLogger`/`WithTracerProvider`/`WithMetricsProvider` and a `WithPillars(*observability.Pillars)` that supplies all three at once. Options apply in order, so `WithPillars(p)` followed by `WithMetricsProvider(nil)` leaves that one component unmetered. Absent means noop: every constructor resolves what it was not given through `logging.EnsureLogger` / `tracing.EnsureTracerProvider` / `metrics.EnsureMetricsProvider`, so a caller that wants no observability names none of it. The four pillar subpackages (`observability/{logging,metrics,tracing,profiling}/config`) have no `WithPillars` and cannot: `observability` imports them to build a `Pillars`.

Go allows one variadic per function, and that slot belongs to the config package's own `Option`. A constructor that also passes options through to what it builds exposes them as `WithXOptions(...)` on that same type — `auditcfg.WithRecorderOptions`, `authorizationcfg.WithStaticOptions` — which is also how one wiring site carries options for whichever of several components it builds.

`do.Provide` registrations resolve observability through `observability.InvokePillars(i)` rather than `do.MustInvoke`, so a container that registers none still wires up. It distinguishes "nobody registered one" (absent, fine) from "the registered one failed to build" (an error), so a misconfigured exporter surfaces instead of degrading to a noop that looks configured.

That registration leaves its type argument inferred — `do.Provide(i, func(i do.Injector) (database.Client, error) {...})`, not `do.Provide[database.Client](i, ...)` — because Go derives it from the provider function's return type, where it is already spelled once. Note that this one is convention only: gopls' `infertypeargs` analyzer flags the explicit form, but golangci-lint ships no equivalent, so `make lint` cannot enforce it and a re-introduced type argument will surface in review or an editor rather than in CI.

`do.ProvideValue` is the opposite case, and the contrast is why the rule is not a blanket "never write a type argument". There inference keys the registration on the value's *concrete* type, so `[T]` is load-bearing wherever the key should be something else — registering a concrete under an interface key, or a typed nil, as in `do.ProvideValue[metrics.Provider](i, nil)`. Omit it only when the value's own type is the key, as in `do.ProvideValue(i, cfg)`.

**Provider packages return their own concrete type** — `memory.NewInMemoryCache` returns `*memory.Cache[T]`, `postgres.NewDatabaseClient` returns `*postgres.Client`, `ssm.NewSecretSource` returns `*ssm.SecretSource` — never the interface it satisfies. Returning the interface is a lossy narrowing at the one point where the caller knows most: they picked this provider, and the interface hands back the union of every provider's failure modes and capabilities in exchange. A caller built on `*memory.Cache[T]` writes no `cache.ErrUnavailable` branch, because that cache has no network to lose; a caller built on `*postgres.Client` reads the native pgx pools off the value, without asserting for `PgxAccess`. A `var _ Iface = (*Impl)(nil)` next to the type keeps conformance a compile-time fact.

This holds for an implementation living beside the interface it satisfies, not only for one in a provider subpackage: `webhooks.NewSQLStore` returns `*webhooks.SQLStore`, `metering.NewSQLStore` returns `*metering.SQLStore`, `shredding.NewKeys` returns `*shredding.KeyManager`. What it does not reach is an interface *method* that returns the interface — `logging.Logger.WithName` hands back a `logging.Logger` because that is what the interface says, so `zap.Logger.SetLevel` is reachable from what `NewZapLogger` returned and not from what `WithName` derived from it. Where that matters, the constructor's doc says so.

The consequence lands on whoever narrows back to the interface, which is mostly the `config` subpackages. `return memory.NewInMemoryCache[T](...)` from a function returning `(cache.Cache[T], error)` converts a nil `*memory.Cache[T]` into a **non-nil** `cache.Cache[T]` on the error path, so a caller testing the result against nil finds a value that panics on first use. Build the provider into a variable and return it only once its error is known to be nil. This applies wherever a concrete-returning constructor feeds an interface-returning one, `do.Provide` blocks included; a constructor that cannot fail (the noops) needs no such care.

There is one deliberate exception outside the config subpackages: `observability.NewObserver(name, logger, tracerProvider)` is positional everywhere. It is the repo-wide DI seam every package's constructor funnels its options into, called once per constructor and never by consumers, and giving it options of its own would mean every package threading options through to build the thing that consumes its options.

Every package uses `WithTracerProvider(tracing.Provider)` — never a ready-made `tracing.Tracer`, which would let a span's instrumentation scope come from the caller instead of the component. `Option` types are **not** parameterized on their package's generic type, even in generic packages (`cache`, `idempotency`, `eventcapture`): Go cannot infer a type argument from a call's result type, so an `Option[T]` forces every call site to spell `T` out forever. Options that genuinely need the type parameter (`WithCodec`, `WithRecordable`, `WithTransform`, `WithObserver`, `WithKeyOrder`) stay generic but infer it from their argument, and the constructor type-asserts and reports a mismatch.

**Extract what can be got wrong twice, not what is merely written twice.** A
second copy of a naming convention, a precision narrowing, a clock read, or a
SQLSTATE list can be *wrong* — it can drift from the first and nothing will say
so — and those get one home: `observability/metrics.OperationSet` and
`observability.Operation.Time` for the instrument trio and its timing,
`internal/pgretry` for the Postgres write-retry loop, `internal/sqlguard` for the
guarded write, `database.ScanAll` for the rows drain, `retry.Full`/`retry.Equal`
for the jitter strategies, `email.FormatAddress` for the RFC-5322 escaping.

A second copy of a *shape* cannot be wrong that way. The provider families —
`llm/{openai,anthropic}`, `embeddings/{openai,ollama,cohere}` — are near-identical
files and are left that way: each is a translation between this module's types
and one vendor's API, so the shape they share is the interface's and the lines
that differ are the ones a reader opened the file for. Factoring them into a base
type with translation hooks trades one readable file for two, at the seam most
likely to move. `llm/doc.go` carries the long form.

The test is whether the copies face the same seam. `featureflags/{posthog,launchdarkly}`
looked like the same case and was not: both evaluate through `*openfeature.Client`,
so only construction and `Close` were vendor-specific and the rest was one
implementation written twice — extracted to `featureflags/internal/openfeatureflags`
and embedded. Two translations to two APIs stay; one implementation against one
seam does not.

## Testing

- **`stretchr/testify` is banned in its entirety** (`assert`, `require`, and `mock`).
  The `depguard` linter enforces this — see `.golangci.yml`. Do not reintroduce
  any testify import.
  - Non-fatal assertions: `github.com/shoenig/test` (package `test`).
    `test.EqOp` for comparable types, `test.Eq` for slices/maps/deep comparison.
    Length/contains helpers have FLIPPED argument order: `test.SliceLen(t, n, slice)`.
  - Fatal assertions: `github.com/shoenig/test/must` (package `must`).
    Same function names as `test`.
  - Mocks: `matryer/moq`, generated from interfaces. See any `<pkg>/mock/doc.go`
    for the `//go:generate` directive pattern (e.g. `authentication/tokens/mock/doc.go`).
- Tests call `t.Parallel()` by default
- Container-backed tests use `testcontainers-go`, live in-package (typically `containers_test.go`),
  and gate on `RUN_CONTAINER_TESTS=true` — they skip otherwise. Stand containers up through the
  `testutils/containers` helpers (`pgtest`, `mysqltest`, `redistest`), not raw testcontainers calls.
- `make test` runs container tests by default and therefore needs a Docker daemon;
  `.scripts/test.sh false` runs the suite without them
- `make test` runs every package (`./...`)
- Test command: `CGO_ENABLED=1 RUN_CONTAINER_TESTS=<true|false> go test -shuffle=on -race -vet=all -failfast`

## Linting

- 42+ linters enabled via `.golangci.yml` (golangci-lint v2 format)
- Formatters: `gci` and `gofmt` (configured in the `formatters:` section)
- Notable strictness: `errcheck`, `errorlint`, `gosec`, `forcetypeassert`, `unconvert`, `unparam`
- Many linters relaxed for `_test.go` files (gosec, goconst, forcetypeassert, unparam, etc.)

# platform-go

[![Go Reference](https://pkg.go.dev/badge/github.com/primandproper/platform-go/v13.svg)](https://pkg.go.dev/github.com/primandproper/platform-go/v13) [![codecov](https://codecov.io/github/primandproper/platform-go/graph/badge.svg?token=69RLLWLJ39)](https://codecov.io/github/primandproper/platform-go)

A Go library providing infrastructure abstractions for cloud-native services. Each package defines a stable interface with one or more provider implementations, selected at runtime via config. Layers that touch the network — HTTP, gRPC, database, messaging — instrument with OpenTelemetry.

**Module:** `github.com/primandproper/platform-go/v13`
**Go:** 1.26

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
go get github.com/primandproper/platform-go/v13@latest
```

Because breaking changes ride the major-version import path, upgrading across majors is an explicit, opt-in edit to your import paths — never a surprise from `go get -u`.

## Design Patterns

**Interface + implementations.** Every major concern is defined as an interface (e.g., `cache.Cache[T]`, `logging.Logger`, `secrets.SecretSource`), with provider implementations in subpackages. Swap implementations via config without touching call sites. Most packages ship a `noop` implementation for tests and for cleanly disabling a concern.

**Config structs.** Each package has a `config` subpackage with `env:`-tagged structs and `ValidateWithContext()` (via `go-ozzo/ozzo-validation`). Configuration is the seam that selects an implementation. Most, but not all, also have `EnsureDefaults()` — packages whose defaults are expressible as `envDefault:` tags use those instead.

Selecting an implementation is deliberate: an unrecognized provider name returns `errors.ErrUnknownProvider` rather than a working-looking noop, because a typo that silently discards every message or never limits a request is a production incident that looks like a healthy process. Where a noop is genuinely wanted it has to be asked for by name.

**OpenTelemetry throughout.** HTTP, gRPC, database, and messaging layers emit traces and metrics. Observability primitives (logging, tracing, metrics, profiling) live under `observability/`.

**Error handling.** Uses [`cockroachdb/errors`](https://github.com/cockroachdb/errors) for rich, wrapped error context. Platform-level sentinel errors live in `errors/`, conventionally imported as `platformerrors`. Transport mappings live in `errors/http` and `errors/grpc`, which import the packages whose sentinels they map — so nothing in those packages may import them back.

## Package Catalog

Implementations are listed in parentheses; most concerns also provide a `noop`.

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
| `sessions`       | Server-side sessions over cookies   | cache, database (+ http)       |
| `authorization`  | Role/permission policy, enforcement | static (default), database     |
| `links`          | Signed, expiring, single-use action links | cache + distributedlock  |
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
| `capitalism`      | Payments                   | stripe                  |
| `entitlements`    | Feature access & remaining quota | —                 |
| `saga`            | Linear durable sagas with compensations | postgres, mysql, sqlite |
| `distributedlock` | Distributed locking        | memory, postgres, redis |
| `workqueue`       | Leased work queue (`SKIP LOCKED` claim/complete/expire) | postgres |
| `timers`          | Durable one-shot scheduling (run once at time T, fleet-wide) | postgres |
| `operations`      | Long-running operations with durable state, two-tier progress, and streamed updates | postgres |
| `filtering`       | Query filters / pagination | —                       |
| `qrcodes`         | QR code generation         | —                       |
| `eventcapture`    | Recording domain events    | jsonl                   |

### Utilities
`errors`, `pointer`, `numbers`, `bitmask`, `charset`, `reflection`, `panicking`, `testutils`, `fake`.

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

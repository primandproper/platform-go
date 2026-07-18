# platform-go

[![Go Reference](https://pkg.go.dev/badge/github.com/primandproper/platform-go/v4.svg)](https://pkg.go.dev/github.com/primandproper/platform-go/v4) [![codecov](https://codecov.io/github/primandproper/platform-go/graph/badge.svg?token=69RLLWLJ39)](https://codecov.io/github/primandproper/platform-go)

A Go library providing infrastructure abstractions for cloud-native services. Each package defines a stable interface with one or more provider implementations, selected at runtime via config. Layers that touch the network — HTTP, gRPC, database, messaging — instrument with OpenTelemetry.

**Module:** `github.com/primandproper/platform-go/v4`
**Go:** 1.26

## Project Status & Stability

> **`main` is not a release channel.** Anything on `main` that has not been cut into a tagged release is considered under active development — alpha/beta, unstable, and unsupported. Treat it as such.

This repository follows a deliberately conservative release model:

- **Only tagged releases are supported.** If it isn't behind a version tag, it can change or break without notice, and no support or compatibility is promised for it.
- **`main` moves ahead of the latest release.** New work — including breaking changes — lands on `main` well before it is deemed release-worthy. The current major module path is `/v4`, but **no `v4` release has been cut yet**; the latest supported release is the most recent `v3.x` tag. A `v4` tag will land only once v4 is judged worth cutting.
- **Semantic Versioning, enforced by Go's module paths.** Breaking changes increment the major version and the module import path (`/v3` → `/v4`), so a major bump can never silently break a consumer that hasn't opted in.
- **No stability guarantees on unreleased APIs.** Interfaces, config shapes, and package boundaries on `main` are subject to change until they ship in a release.

If you depend on this library, pin to a released tag. If you want to track upcoming work, `main` is fair game — just don't expect it to hold still.

## Installation

```bash
go get github.com/primandproper/platform-go/v4@latest
```

Because breaking changes ride the major-version import path, upgrading across majors is an explicit, opt-in edit to your import paths — never a surprise from `go get -u`.

## Design Patterns

**Interface + implementations.** Every major concern is defined as an interface (e.g., `cache.Cache[T]`, `logging.Logger`, `secrets.SecretSource`), with provider implementations in subpackages. Swap implementations via config without touching call sites. Most packages ship a `noop` implementation for tests and for cleanly disabling a concern.

**Config structs.** Each package has a `config` subpackage with `env:`-tagged structs, `ValidateWithContext()` (via `go-ozzo/ozzo-validation`), and `EnsureDefaults()`. Configuration is the seam that selects an implementation.

**OpenTelemetry throughout.** HTTP, gRPC, database, and messaging layers emit traces and metrics. Observability primitives (logging, tracing, metrics, profiling) live under `observability/`.

**Error handling.** Uses [`cockroachdb/errors`](https://github.com/cockroachdb/errors) for rich, wrapped error context. Platform-level sentinel errors live in `internal`/`errors`.

## Package Catalog

Implementations are listed in parentheses; most concerns also provide a `noop`.

### Data & storage
| Package    | Purpose                              | Implementations                       |
|------------|--------------------------------------|---------------------------------------|
| `database` | SQL access + instrumentation         | postgres, mysql, sqlite               |
| `cache`    | Generic key/value cache (`Cache[T]`) | redis, memory                         |
| `uploads`  | Blob/object storage & image handling | objectstorage (S3-compatible), images |
| `files`    | Filesystem & streaming helpers       | —                                     |
| `secrets`  | Secret sourcing                      | env, gcp, ssm, kubectl                |

### Messaging & events
| Package         | Purpose                    | Implementations                                   |
|-----------------|----------------------------|---------------------------------------------------|
| `messagequeue`  | Publish/subscribe & queues | kafka, pubsub, redis, sqs                         |
| `eventstream`   | Server push to clients     | sse, websocket                                    |
| `notifications` | User notifications         | async, mobile                                     |
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

### Observability & operations
| Package         | Purpose                              | Implementations                                    |
|-----------------|--------------------------------------|----------------------------------------------------|
| `observability` | Logging, tracing, metrics, profiling | logging (slog, zap, zerolog); OTel tracing/metrics |
| `healthcheck`   | Health/readiness checks              | —                                                  |
| `version`       | Build/version metadata               | —                                                  |

### Auth & security
| Package          | Purpose                        | Implementations      |
|------------------|--------------------------------|----------------------|
| `authentication` | Password hashing, TOTP, tokens | argon2, totp, tokens |
| `cryptography`   | Cryptographic primitives       | —                    |
| `random`         | Secure randomness              | —                    |
| `identifiers`    | ID generation                  | —                    |

### AI, ML & product
| Package        | Purpose                      | Implementations               |
|----------------|------------------------------|-------------------------------|
| `llm`          | Large language model clients | anthropic, openai             |
| `embeddings`   | Embedding generation         | —                             |
| `search`       | Vector / text search         | vector, text                  |
| `analytics`    | Product analytics            | posthog, segment, multisource |
| `featureflags` | Feature flagging             | launchdarkly, posthog         |

### Domain & coordination
| Package           | Purpose                    | Implementations         |
|-------------------|----------------------------|-------------------------|
| `capitalism`      | Payments                   | stripe                  |
| `distributedlock` | Distributed locking        | memory, postgres, redis |
| `filtering`       | Query filters / pagination | —                       |
| `qrcodes`         | QR code generation         | —                       |
| `artifacts`       | Artifact handling          | —                       |

### Utilities
`errors`, `pointer`, `numbers`, `bitmask`, `reflection`, `panicking`, `testutils`, `fake`.

## Development

```bash
make setup          # Install dev tools and vendor deps
make format         # Format all Go code (imports, field/tag alignment, gofmt)
make lint           # Run golangci-lint (Docker) + shellcheck
make test           # Run tests (race detector, shuffle, failfast)
make build          # Build all packages
make generate       # Regenerate moq mocks after changing a mocked interface
make revendor       # Clean and re-vendor dependencies
```

Formatting runs locally with `gci`, `goimports`, `fieldalignment`, `tagalign`, and `gofmt`. Linting runs in Docker against the `golangci/golangci-lint` image (42+ linters, golangci-lint v2 format).

### Testing conventions

- **`stretchr/testify` is banned** (`assert`, `require`, and `mock`), enforced by `depguard`. Use [`shoenig/test`](https://github.com/shoenig/test) for assertions (`test` for non-fatal, `must` for fatal) and [`matryer/moq`](https://github.com/matryer/moq) for mocks.
- Tests run in parallel by default and use subtests throughout.
- Integration tests use `testcontainers-go`, live in separate directories, and are excluded from `make test`.
- `make test` runs `CGO_ENABLED=1 go test -shuffle=on -race -vet=all -failfast`, excluding `cmd`, integration, mock, fake, converter, util, and generated packages.

## Contributing

Because `main` is a development channel and only tagged releases are supported, changes land on `main` freely and are stabilized before release. Follow the existing package layout (interface + config subpackage + provider implementations + `noop`), match the surrounding code, and keep `make format lint test` green.

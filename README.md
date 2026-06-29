# platform-go

[![Go Reference](https://pkg.go.dev/badge/github.com/primandproper/platform-go/v2.svg)](https://pkg.go.dev/github.com/primandproper/platform-go/v2) [![codecov](https://codecov.io/github/primandproper/platform-go/graph/badge.svg?token=69RLLWLJ39)](https://codecov.io/github/primandproper/platform-go)

A Go library providing infrastructure abstractions for cloud-native services. Each package defines a stable interface with multiple provider implementations, selected at runtime via config. All packages instrument with OpenTelemetry where applicable.

**Module:** `github.com/primandproper/platform-go/v2`
**Go:** 1.26

## Design Patterns

**Interface + implementations:** Every major concern is defined as an interface in the root package (e.g., `cache.Cache[T]`), with provider implementations in subpackages. Swap implementations via config without changing call sites.

**Config structs:** Each package has a `config` subpackage with `env:`-tagged structs, `ValidateWithContext()` validation, and `EnsureDefaults()`.

**OTel throughout:** HTTP, gRPC, database, and messaging layers are instrumented for traces and metrics.

**Error handling:** Uses [`cockroachdb/errors`](https://github.com/cockroachdb/errors) for rich error context.

## Development

```bash
make format         # Format all Go code
make lint           # Run golangci-lint (Docker) + shellcheck
make test           # Run tests with race detector and shuffle
make build          # Build all packages
make setup          # Install dev tools and vendor deps
make revendor       # Clean and re-vendor dependencies
```

Linting runs in Docker (`golangci/golangci-lint`). Formatting uses `gci`, `goimports`, `fieldalignment`, `tagalign`, and `gofmt` locally.

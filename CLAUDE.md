# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Go library/framework (`github.com/primandproper/platform-go`) providing infrastructure abstractions for cloud-native services: database, caching, messaging, observability, secrets, uploads, email, payments, and more. Single module, ~40 packages, Go 1.26.

## Common Commands

```bash
make format         # Format all Go code (imports, field alignment, tag alignment, gofmt)
make lint           # Run golangci-lint (Docker) + shellcheck
make format lint    # Typical workflow: format then lint
make test           # Run tests (race detector, shuffle, failfast)
make build          # Build all packages
make generate       # Regenerate moq mocks after changing any mocked interface
make setup          # Install dev tools + vendor deps
make revendor       # Clean and re-vendor dependencies
```

Run a single test:
```bash
go test -run TestFunctionName ./package/path/...
```

Run tests for a single package:
```bash
go test -race ./cache/...
```

Linting runs in Docker (`golangci/golangci-lint` image). Formatting runs locally with `gci`, `goimports`, `fieldalignment`, `tagalign`, and `gofmt`.

## Import Ordering

Import ordering uses `gci` with four sections, separated by blank lines:

1. Standard library
2. `github.com/primandproper/platform-go` (this module)
2. `github.com/primandproper` (org-level packages)
4. Everything else (third-party)

The Makefile `THIS` variable must be the full module path (`github.com/primandproper/platform-go`) because `format_imports.sh` uses `dirname` to derive the org prefix. If `THIS` is too short, `dirname` produces `github.com` which creates a spurious `prefix(github.com)` gci section.

## Architecture Patterns

**Interface + multi-implementation:** Most packages define an interface with multiple implementations selected by config. Examples: `cache.Cache[T]` (Redis, memory), `logging.Logger` (slog, zap, zerolog), `secrets.SecretSource` (env, GCP, AWS SSM), `uploads` (S3, GCS, filesystem).

**Config structs:** Each major package has a `config` subpackage using `env:` struct tags, `ValidateWithContext()` via `go-ozzo/ozzo-validation`, and `EnsureDefaults()`.

**OpenTelemetry throughout:** Database, HTTP, gRPC, and messaging all instrument with OTel for traces, metrics, and logs.

**Error handling:** Uses `cockroachdb/errors` for rich error context. Platform-level sentinels defined in `internalerrors/`.

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
  - The `/convert-assertions` and `/convert-mocks` skills document the migration
    rules in detail.
- Tests call `t.Parallel()` by default
- Integration tests use `testcontainers-go` and live in separate directories excluded from `make test`
- `make test` excludes: cmd, integration, mock, fakes, converters, utils, generated packages
- Test command: `CGO_ENABLED=1 go test -shuffle=on -race -vet=all -failfast`

## Linting

- 42+ linters enabled via `.golangci.yml` (golangci-lint v2 format)
- Formatters: `gci` and `gofmt` (configured in the `formatters:` section)
- Notable strictness: `errcheck`, `errorlint`, `gosec`, `forcetypeassert`, `unconvert`, `unparam`
- Many linters relaxed for `_test.go` files (gosec, goconst, forcetypeassert, unparam, etc.)

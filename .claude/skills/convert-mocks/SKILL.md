---
name: convert-mocks
description: >
  Convert testify mock usage to matryer/moq in a single Go package of this
  repo. Generates new moq files in shared mock/ packages as needed (additively
  — never deletes testify types). Paired with /convert-assertions; either can
  run first. Invoke as /convert-mocks <package-path>, e.g.
  /convert-mocks ./ratelimiting/redis/.
---

# convert-mocks

Migrate a single Go package's tests from `stretchr/testify/mock` (and hand-written testify-based mock packages) to `github.com/matryer/moq`. This is Phase 2 of the shoenig/moq migration, executed one package at a time. Assertions are out of scope — see `/convert-assertions` for that half. Either skill can run before the other; the file becomes a hybrid during a partial migration and that's fine.

## Argument

The user invokes this with a package path relative to the repo root (e.g. `./ratelimiting/redis/`). That's the target. Work only inside it, with one permitted exception: you may ADD (never delete or modify) files inside a shared `<pkg>/mock/` sub-package to generate a moq mock the target depends on.

## Hard rules — do not violate

1. **Do NOT delete testify mock types from shared mock packages.** ~30+ other consumers may still rely on them. The testify types die in the final PR of the migration, not in this one. Every `<pkg>/mock/` package during the transition holds BOTH the hand-written testify types and the moq-generated types side by side.
2. **Do NOT modify existing hand-written testify mock files** (e.g., `circuitbreaking/mock/mock.go`, `observability/metrics/mock/provider.go`). Only ADD new `*_mock.go` files or update `doc.go` to include a new `//go:generate` directive.
3. **Do NOT introduce `mock2` naming.** Phase 0 of the migration killed that experiment. moq files live directly in the canonical `mock/` packages alongside testify files, with distinct type names (`CircuitBreakerMock` vs `MockCircuitBreaker`, `ProviderMock` vs `MetricsProvider`).
4. **Do NOT touch assertions.** Leave every `assert.*`, `require.*`, `test.*`, and `must.*` call exactly as you found it. If you need to add new assertions (e.g., verifying call counts after dropping `AssertExpectationsForObjects`), write them in shoenig form (`test.SliceLen(t, n, mock.XCalls())`) — shoenig is the repo's target state. If the file doesn't already import `github.com/shoenig/test`, add the import.
5. **Do NOT export internal test-seam interfaces.** If the target has an unexported interface like `redisClient` used only for mock injection in tests, keep its moq mock INLINE in the target package (a `*_test.go` file with `//go:generate` + `-skip-ensure` + alias syntax). Don't move it into a sibling `mock/` package.
6. **Do NOT move or split tests.** File layout cleanup is a separate concern.
7. **Do NOT compile or leave binaries.** Use `go vet` / `go test` / `go build` without `-o`. If you do produce a binary for any reason, delete it immediately.
8. **Do NOT add empty method stubs** to satisfy moq generation. See `feedback_no_empty_methods.md` in user memory. If moq generates `XxxFunc` fields the test doesn't need, leave them nil — calling a nil `XxxFunc` panics, which is the correct failure mode for "this method must not be called."
9. **One package per invocation.** Never convert multiple packages in one run.

## Preflight

Before editing anything:

1. Read `CLAUDE.md` at the repo root if you haven't this session.
2. Read `project_moq_shoenig_pilot.md` in user memory — critical for the naming quirks (e.g., `mockmetrics` package name being non-standard).
3. `ls <target>/` to know what test files exist.
4. `Grep` for `stretchr/testify/mock`, `testify/mock`, `.On(`, `.Return(`, `mock.Anything`, `AssertExpectations`, and any `<someMock>.Mock` struct-literal pattern within the target to enumerate what needs rewriting.
5. `Grep` for imports of shared mock packages (e.g., `circuitbreaking/mock`, `observability/metrics/mock`, `encoding/mock`, `messagequeue/mock`, `routing/mock`, `uploads/mock`, `uploads/images/mock`) to know which shared mocks the target relies on.
6. Baseline: `go build ./<target>/... && CGO_ENABLED=1 go test -race -vet=all -shuffle=on ./<target>/... 2>&1`. Must pass before any edit. If broken, stop and tell the user.

If no testify mock usage surfaces in preflight, report "nothing to do" and exit.

## Step 1: inventory the mocks the target uses

Categorize every mock the target references into one of four buckets. The handling differs per bucket.

**Bucket A: moq version already exists in the shared mock package.** No generation needed; just flip call sites.

Known-good as of the pilot:

| Shared package | Testify type | Moq type | Import |
|---|---|---|---|
| `circuitbreaking/mock` | `MockCircuitBreaker` | `CircuitBreakerMock` | `mockcircuitbreaking "github.com/primandproper/platform-go/circuitbreaking/mock"` (alias required — package name is `mock`) |
| `observability/metrics/mock` | `MetricsProvider` | `ProviderMock` | `"github.com/primandproper/platform-go/observability/metrics/mock"` (no alias — package name is already `mockmetrics`) |

To confirm a shared mock package has a moq version: look for a `*_mock.go` file (not `_test.go`) in the package, or grep for `//go:generate go tool github.com/matryer/moq` in its `doc.go`.

**Bucket B: moq version does not exist yet in the shared mock package, must be generated.** This is the "additive" workflow:

1. Read the interface declaration in the parent package (e.g., read `encoding/encoding.go` to find the interface definition for a mock needed from `encoding/mock`).
2. Open the shared mock package's `doc.go` — create one if it doesn't exist.
3. Add (or append) a `//go:generate` directive:
   ```go
   //go:generate go tool github.com/matryer/moq -out <iface_snake>_mock.go -pkg <mock-package-name> -rm -fmt goimports .. <InterfaceName>:<InterfaceName>Mock
   ```
   - `<mock-package-name>` must match the existing `package` declaration in the mock package's files. For `circuitbreaking/mock` it's `mock`; for `observability/metrics/mock` it's `mockmetrics`; for others, check.
   - The source-dir `..` assumes the interface lives in the parent package of `mock/`. If the interface is elsewhere, adjust the relative path.
   - The alias `InterfaceName:InterfaceNameMock` gives the mock a suffix-style name that won't collide with the testify prefix-style names (e.g., `MockCircuitBreaker` vs `CircuitBreakerMock`).
4. Run `go generate ./<shared-pkg>/mock/` to produce the file.
5. Verify the generated file compiles: it should contain a `var _ <pkg>.<InterfaceName> = &<InterfaceName>Mock{}` ensure-line.

**Bucket C: the target defines its own local testify mock inline** (e.g., a `mockRedisClient struct { mock.Mock }` in a `*_test.go` file). This is the unexported-interface-inline case:

1. Identify the interface being mocked (in the target's source, not a test file).
2. If the interface is UNEXPORTED, keep the mock inline. Add a `mocks_gen_test.go` file (or update an existing one) with a `//go:generate` directive pointing at the current directory and using `-skip-ensure` + alias if the interface is unexported:
   ```go
   //go:generate go tool github.com/matryer/moq -out <iface>_mock_test.go -pkg <target-pkg> -rm -skip-ensure -fmt goimports . <interfaceName>:<InterfaceName>Mock
   ```
   Use `-skip-ensure` only for unexported interfaces. For exported interfaces in the same package, drop `-skip-ensure`.
3. Run `go generate ./<target>/`.
4. Delete the old hand-written `mockFoo struct { mock.Mock }` definition and any methods on it.

**Bucket D: the target uses a generic `stretchr/testify/mock.Mock` directly without a wrapper** — rare. Usually a sign someone cut a corner. Treat as Bucket C: identify what it's pretending to be, define a proper interface if needed, generate a moq.

## Step 2: rewrite call sites

For each testify mock usage in the target's test files:

### Struct instantiation

| Testify | moq |
|---|---|
| `m := &mockcircuitbreaking.MockCircuitBreaker{}` | `m := &mockcircuitbreaking.CircuitBreakerMock{}` |
| `m := &mockmetrics.MetricsProvider{}` | `m := &mockmetrics.ProviderMock{}` |
| `m := &mockRedisClient{}` (local) | `m := &redisClientMock{}` (whatever moq's alias produced) |

### Method expectations — `.On(...).Return(...)` → `XxxFunc` closures

The single biggest conceptual shift. Testify sets up expectations at construction time; moq sets a function field that runs whenever the method is called.

**Simple return value:**
```go
// testify
m.On("CannotProceed").Return(false)

// moq
m.CannotProceedFunc = func() bool { return false }
```

**Void return:**
```go
// testify
m.On("Succeeded").Return()

// moq
m.SucceededFunc = func() {}
```

**Return with computed value based on args:**
```go
// testify (using argument matchers)
client.On("Get", mock.Anything, "expected-key").Return(someResult, nil)

// moq
client.GetFunc = func(_ context.Context, key string) (ResultType, error) {
    test.EqOp(t, "expected-key", key)
    return someResult, nil
}
```

**Different returns for different inputs — prefer a dispatch map:**
```go
// testify
m.On("NewInt64Counter", "name_hits", /* opts */).Return(okCounter, nil)
m.On("NewInt64Counter", "name_misses", /* opts */).Return(nil, errors.New("fail"))

// moq — declare the dispatch map, then the closure reads from it
results := map[string]struct{ counter metrics.Int64Counter; err error }{
    "name_hits":   {counter: okCounter},
    "name_misses": {counter: nil, err: errors.New("fail")},
}
m.NewInt64CounterFunc = func(metricName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
    res, ok := results[metricName]
    if !ok { t.Fatalf("unexpected NewInt64Counter call: %q", metricName) }
    return res.counter, res.err
}
```

See `cache/redis/redis_test.go`'s `newCounterProviderMock` helper for this pattern in action.

**Sequential returns for the same args (rare):**
```go
// testify
m.On("X").Return(first).Once()
m.On("X").Return(second).Once()

// moq — stateful closure with a counter
var calls int
m.XFunc = func() ResultType {
    calls++
    if calls == 1 { return first }
    return second
}
```

### Argument matchers

| Testify | moq |
|---|---|
| `mock.Anything` | `_` in closure param list (ignored) |
| `mock.MatchedBy(func(x X) bool { return predicate })` | explicit check inside closure body |
| `testutils.ContextMatcher` | `_ context.Context` in closure (ignored) |
| `testutils.QueryFilterMatcher` | `_ *filtering.QueryFilter` in closure (ignored) |
| any literal value (e.g., `"foo"`, `42`) | explicit check inside closure: `test.EqOp(t, "foo", got)` |

The closure IS the matcher. There is no matcher library.

### Expectation verification — `AssertExpectations*` and `.Times()`

| Testify | moq equivalent |
|---|---|
| `mock.AssertExpectationsForObjects(t, m)` | usually drop. The functional check (did the code return the right result?) already proves the mock was invoked. Only add explicit call-count checks if the test was specifically verifying invocation count. |
| `m.AssertCalled(t, "Method", args)` | `test.SliceLen(t, ≥1, m.MethodCalls())` and optionally inspect `m.MethodCalls()[i]` for args |
| `m.AssertNotCalled(t, "Method")` | `test.SliceLen(t, 0, m.MethodCalls())` |
| `.Times(n)` set at expectation time | `test.SliceLen(t, n, m.MethodCalls())` at end of test |
| `.Once()` set at expectation time | `test.SliceLen(t, 1, m.MethodCalls())` at end of test |

`m.XxxCalls()` returns a slice of typed structs, one element per call, with fields matching the method's parameters (minus variadics which get a slice field). You can index into it to assert per-call arguments if needed.

If the new call-count assertion requires `github.com/shoenig/test`, add the import. Don't be shy about it — shoenig is the repo's target state even for the mock-migrated half.

## Step 3: imports

After editing, the file's imports may need adjustment:

- **Remove** `"github.com/stretchr/testify/mock"` if there are no more `mock.Anything`, `mock.AssertExpectations*`, or similar references.
- **Remove** the old shared mock import alias (`mockcircuitbreaking "..."`) only if you're flipping every usage in the file; if some references still reach for `MockCircuitBreaker` (e.g., a shared helper in the same file is still testify-bound), keep the import.
- **Add** `"github.com/shoenig/test"` (and/or `/must`) if you introduced new shoenig assertions for call-count verification.

Import ordering follows `gci --custom-order`:

1. std
2. `prefix(github.com/primandproper/platform-go)` — blank-line separator after
3. `prefix(github.com/primandproper/platform-go)` — org-level (usually empty)
4. default (third-party)

## Verification

Every check must pass. Run in this order:

```bash
go build ./<target>/... 2>&1
go vet ./<target>/... 2>&1
CGO_ENABLED=1 go test -race -vet=all -shuffle=on ./<target>/... 2>&1
gofmt -l <target>/
```

If you touched a shared mock package (Bucket B) or generated inline moqs (Bucket C), also verify generation is idempotent:

```bash
make generate 2>&1
git status --porcelain
```

The diff after `make generate` should match what you intended and include NO surprise changes to other packages. Run `make generate` a second time and confirm no new drift.

gci format check:

```bash
go tool github.com/daixiang0/gci diff --skip-generated --custom-order \
  --section standard \
  --section "prefix(github.com/primandproper/platform-go)" \
  --section "prefix(github.com/primandproper/platform-go)" \
  --section default \
  <target>/
```

If Docker is available, lint:

```bash
docker run --rm --volume "$PWD:$PWD" --workdir="$PWD" \
  golangci/golangci-lint:v2.10.1 \
  golangci-lint run --timeout 5m ./<target>/...
```

If you touched a shared mock package, also lint it:
```bash
docker run ... golangci-lint run --timeout 5m ./<shared-pkg>/mock/...
```

Must report `0 issues.`

## Pitfalls that burned the pilot

- **`mockmetrics` is the package name; the directory is `mock`.** Import `observability/metrics/mock` WITHOUT an alias; the identifier you use in code is `mockmetrics.ProviderMock`. Adding an alias is redundant and linters may flag it.
- **`circuitbreaking/mock` package name is `mock`.** You need an alias (`mockcircuitbreaking`) to avoid ambiguity when multiple files or packages in scope could shadow the identifier.
- **`-skip-ensure` is only for unexported interfaces.** Do not use it for exported ones — you'd lose the compile-time interface-satisfaction check.
- **moq's generated files trip `fieldalignment`** on anonymous struct returns (the `XxxCalls()` return type). Harmless: `fieldalignment -fix` returns 0, `make format` converges, and `golangci-lint` skips generated files via `exclusions.generated: lax`. Don't try to "fix" the generated file.
- **LSP diagnostics lag.** After deletes or moves, the IDE may report phantom errors. Trust `go build` — if it exits 0, the diagnostics are stale.
- **Don't break tests to make the mock pass.** If moq's nil-XxxFunc-panics behavior is firing during a test run, the test was exercising a code path that the testify version silently ignored. Either add the missing `XxxFunc` to the setup (if the call is legitimate) or understand why it's happening (the code under test may have changed).
- **The new call-count check replaces AssertExpectations semantically, not behaviorally.** testify's AssertExpectations fails if unexpected methods are called; moq's `XCalls()` length check only verifies the count of the one method you checked. If a test relied on "fail loudly if anything unexpected happens", you may need to explicitly set `XxxFunc = func(...) { t.Fatalf("unexpected call to X") }` for every method that should NOT be called.
- **Test file imports may look wrong right after a rewrite.** gci will reorder them when `make format` runs; gofmt will handle whitespace. But if the file doesn't compile, trust the compile error over the LSP.

## Report format

Under 40 lines.

1. **Files modified** — grouped by: target test files touched, shared mock packages extended (if any), inline mocks added (if any).
2. **Generated files added** — list any new `*_mock.go` or `*_mock_test.go` files.
3. **Counts** — approximate: `.On/.Return → XxxFunc: N`, `AssertExpectations dropped: M`, `mock types renamed: K`.
4. **Bucket breakdown** — how many mocks fell into each bucket (A: already existed, B: generated into shared, C: inline generated).
5. **What WASN'T touched** — confirm assertions were left alone (testify assertions may still be present if `/convert-assertions` hasn't run on this package).
6. **Verification results** — ✓ on each check, or what failed.
7. **Manual follow-ups the user should review** — judgment calls on: dropped AssertExpectations that may have been exhaustive checks, dispatch maps where the mapping is non-obvious, any sequential-return translations, any new `t.Fatalf` guards added to catch "should not be called" cases.
8. **Git status summary** — short list of modified + untracked files.

## Edge cases

- **Target has no testify mocks** — report "nothing to do" and exit.
- **Target uses a mock from a shared package, and that package has multiple mock types (e.g., `observability/metrics/mock` has `MetricsProvider` AND `Int64Counter`), but the target only uses one** — only flip the one the target uses. Don't migrate the others preemptively; let another consumer's run of this skill pick them up.
- **Target has a shared helper** (e.g., `helpers_test.go`) that takes a `*mockX.MockFoo` parameter and is called from files in sub-packages — this is a widening change. Stop, describe the shared helper and its callers, and ask the user whether to (a) migrate the helper signature + all callers in one PR, (b) add an interface-typed parameter so both old and new mocks satisfy it during transition, or (c) skip this target and pick one without a cross-package helper entanglement.
- **Target has a TestMain or init() that sets up global mocks** — rare; if encountered, flag it and proceed carefully.
- **Target has generic interfaces** (like `cache.Cache[T]`) — moq handles generics. The generated mock will be `CacheMock[T any]`, call sites write `&mock.CacheMock[ConcreteType]{}`. If generation is needed, proceed normally; if call sites need the type parameter added, flag it in the report.
- **Target tests are a mix of real-backend (testcontainers) and mock-backed (testify/moq)** — convert only the mock-backed ones. Container tests don't use mocks.

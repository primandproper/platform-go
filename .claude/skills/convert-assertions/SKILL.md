---
name: convert-assertions
description: >
  Mechanical rewrite of testify/{assert,require} to shoenig/{test,must} in a
  single Go package of this repo. Does NOT touch mocks, file layout, or any
  other package. Invoke as /convert-assertions <package-path>, e.g.
  /convert-assertions ./ratelimiting/redis/. Paired with /convert-mocks which
  handles the testify/mock → matryer/moq half of the migration independently.
---

# convert-assertions

Migrate a single Go package's test assertions from `testify/{assert,require}` to `github.com/shoenig/test` (non-fatal, package name `test`) + `github.com/shoenig/test/must` (fatal). This is Phase 1 of the shoenig/moq migration, executed one package at a time. Mocks are out of scope — see `/convert-mocks` for that half.

## Argument

The user invokes this with a package path relative to the repo root (e.g. `./ratelimiting/redis/`). That's the target. Work only inside it.

## Hard rules — do not violate

1. **Do NOT touch mocks.** Leave every `stretchr/testify/mock` import, `.On(...)`, `.Return(...)`, `mock.Anything`, `mock.MatchedBy(...)`, `mock.AssertExpectationsForObjects(...)`, and `<pkg>/mock.Mock<Foo>{}` call site exactly as-is. If a test file mixes testify mocks and testify assertions, this skill converts only the assertions; the file becomes a hybrid that still compiles. `/convert-mocks` will clean up the mock half later.
2. **Do NOT touch other packages.** Only edit `*_test.go` files inside the target package.
3. **Do NOT move or split tests.** File layout cleanup is a separate concern; it's not this skill's job.
4. **Do NOT rename tests or subtests.** No changing `Test_Foo_Unit` → `Test_Foo`, no dropping `_Unit` suffixes. Preserve existing naming.
5. **Do NOT use sed/awk/grep-replace.** Use the `Edit` tool with exact strings. The API has traps (length-first argument order on `SliceLen`/`MapLen`, `Eq` vs `EqOp` distinction) that a dumb find/replace will get wrong.
6. **Do NOT compile or leave binaries.** Verify via `go vet` and `go test`. No `go build -o` without immediate cleanup.
7. **Preserve `T` vs `t`.** Repo convention: top-level test functions take `T *testing.T`, subtests take `t *testing.T`. Don't touch this. See `feedback_test_variable_naming.md` in user memory.
8. **One package per invocation.** Never go beyond the target, even if you notice neighboring packages would benefit.

## Preflight

Before editing anything:

1. Read `CLAUDE.md` at the repo root if you haven't this session — it has the module path and import ordering rules.
2. Read `project_moq_shoenig_pilot.md` in user memory — it captures the per-quirk details about the pilot.
3. `ls <target>/` to know what test files exist.
4. `Grep` for `stretchr/testify/assert` and `stretchr/testify/require` within the target to enumerate files that need rewriting. If neither appears, report back immediately — nothing to do.
5. Baseline: `go build ./<target>/... && CGO_ENABLED=1 go test -race -vet=all -shuffle=on ./<target>/... 2>&1`. It must pass BEFORE any edit. If the baseline is broken, stop and tell the user — the breakage isn't yours to fix under this skill.

## Import rewrites

| Testify import | Shoenig replacement |
|---|---|
| `"github.com/stretchr/testify/assert"` | `"github.com/shoenig/test"` (package name: `test`) |
| `"github.com/stretchr/testify/require"` | `"github.com/shoenig/test/must"` (package name: `must`) |

**There is no `github.com/shoenig/test/test` subpackage.** The non-fatal package lives at the module root with package name `test`. If you ever write the import path `github.com/shoenig/test/test`, you made a mistake — `go build` will tell you immediately. The correct form is:

```go
import (
    "github.com/shoenig/test"         // for test.Eq, test.NoError, etc (non-fatal)
    "github.com/shoenig/test/must"    // for must.NoError, must.NotNil, etc (fatal)
)
```

If a file used only `assert`, it gets only the root import. If a file used only `require`, it gets only `/must`. If it used both, it gets both imports.

## Call-site rewrites

`assert.*` → `test.*` (non-fatal). `require.*` → `must.*` (fatal). The function names match between `test` and `must` — so `assert.NoError` → `test.NoError`, `require.NoError` → `must.NoError`.

### Direct one-to-one mappings

| Testify | Shoenig | Notes |
|---|---|---|
| `assert.NoError(t, err)` | `test.NoError(t, err)` | |
| `require.NoError(t, err)` | `must.NoError(t, err)` | |
| `assert.Error(t, err)` | `test.Error(t, err)` | |
| `require.Error(t, err)` | `must.Error(t, err)` | |
| `assert.ErrorIs(t, err, target)` | `test.ErrorIs(t, err, target)` | |
| `assert.EqualError(t, err, "msg")` | `test.EqError(t, err, "msg")` | name change: `EqualError` → `EqError` |
| `assert.Nil(t, x)` | `test.Nil(t, x)` | |
| `assert.NotNil(t, x)` | `test.NotNil(t, x)` | |
| `assert.True(t, x)` | `test.True(t, x)` | |
| `assert.False(t, x)` | `test.False(t, x)` | |

### Equality — `Eq` vs `EqOp`

`assert.Equal` has two shoenig equivalents and choosing the right one matters:

- **`test.EqOp(t, want, got)`** — uses Go `==`. Only works for **comparable** types: strings, numbers, bools, pointers, `time.Duration`, channels, and structs whose fields are all comparable. Faster, catches type mismatches at compile time. Prefer this when possible.
- **`test.Eq(t, want, got)`** — uses reflect/go-cmp. Works on slices, maps, structs with non-comparable fields, pointers-to-structs where you want deep comparison.

**Rules of thumb:**
- Primitives (`string`, `int`, `bool`, `time.Duration`) → `EqOp`
- Named comparable types (e.g., custom string types, enum-like consts) → `EqOp`
- Slices or maps → `Eq` (`EqOp` won't compile)
- Structs → usually `Eq` (safer); use `EqOp` only if the struct is explicitly comparable and small
- Pointers-to-structs — you almost always want the underlying struct value compared → `test.Eq(t, want, got)` works (go-cmp handles it)
- Errors — prefer `test.ErrorIs(t, err, target)` or `test.EqError(t, err, "msg string")` over `Eq`/`EqOp` on error values directly

If unsure, use `Eq`. If lint/tests complain with `EqOp` (type is not comparable), switch to `Eq`.

### Length and collection checks — argument order is FLIPPED

⚠️ **This is the single biggest trap in the migration.** In testify, length comes AFTER the collection. In shoenig, length comes FIRST.

| Testify | Shoenig |
|---|---|
| `assert.Len(t, slice, 3)` | `test.SliceLen(t, 3, slice)` |
| `assert.Len(t, m, 3)` (where `m` is a map) | `test.MapLen(t, 3, m)` |
| `assert.Empty(t, slice)` | `test.SliceEmpty(t, slice)` |
| `assert.Empty(t, m)` (map) | `test.MapEmpty(t, m)` |
| `assert.Contains(t, slice, elem)` | `test.SliceContains(t, slice, elem)` |
| `assert.Contains(t, str, substr)` | `test.StrContains(t, str, substr)` |
| `assert.Contains(t, m, key)` (map) | `test.MapContainsKey(t, m, key)` |
| `assert.NotContains(t, slice, elem)` | `test.SliceNotContains(t, slice, elem)` |

**There is no polymorphic `Len` or `Contains` or `Empty`.** You must pick the right variant based on the collection type. When the existing testify call is `assert.Len(t, x, n)` and you can't immediately tell whether `x` is a slice or a map, read the surrounding code: check where `x` is declared or assigned.

### Less common but worth knowing

| Testify | Shoenig |
|---|---|
| `assert.NotEqual(t, a, b)` | `test.NotEq(t, a, b)` |
| `assert.Same(t, a, b)` (same pointer) | `test.Eq(t, a, b)` works; no distinct `Same` in shoenig |
| `assert.IsType(t, expected, actual)` | `test.EqOp(t, reflect.TypeOf(expected), reflect.TypeOf(actual))` or use a type assertion + `test.True` |
| `assert.Panics(t, f)` | shoenig does not have a direct equivalent — rare; if encountered, flag in the report and leave the testify call alone, or implement with `defer recover()` inline |
| `assert.Fail(t, "msg")` | `t.Fatalf("msg")` or `t.Errorf("msg")` — use stdlib |
| `assert.FailNow(t, "msg")` | `t.Fatalf("msg")` — use stdlib |
| `assert.Greater(t, a, b)` | shoenig's `test.Greater(t, b, a)` — verify argument order in shoenig docs before committing; if uncertain, use `test.True(t, a > b)` |
| `assert.Less(t, a, b)` | same caveat — use `test.True(t, a < b)` if uncertain |

If you encounter a testify assertion not listed here, do ONE of:
1. Use `test.True(t, condition)` with the condition inlined.
2. Flag it in the report and leave the testify call untouched so the user can decide.

Do not guess a name you're not confident about.

## Import ordering (gci with --custom-order)

After editing, the final import block must satisfy gci's custom-order rules:

1. Standard library
2. `prefix(github.com/primandproper/platform-go)` — this module
3. `prefix(github.com/primandproper/platform-go)` — org-level (usually empty for this repo)
4. default — third-party

shoenig/test imports go in section 4 (third-party), separated from the platform imports by a blank line. Example:

```go
import (
    "context"
    "testing"

    "github.com/primandproper/platform-go/some/package"

    "github.com/shoenig/test"
    "github.com/shoenig/test/must"
)
```

The `Edit` tool doesn't auto-reorder imports. If you're moving imports between sections, do the edit explicitly and then run the verification step below — it includes `gci diff` which will catch ordering mistakes.

## Verification

Every check must pass before you declare success. Run them in this order:

```bash
go build ./<target>/... 2>&1
go vet ./<target>/... 2>&1
CGO_ENABLED=1 go test -race -vet=all -shuffle=on ./<target>/... 2>&1
gofmt -l <target>/
go tool github.com/daixiang0/gci diff --skip-generated --custom-order \
  --section standard \
  --section "prefix(github.com/primandproper/platform-go)" \
  --section "prefix(github.com/primandproper/platform-go)" \
  --section default \
  <target>/
```

If Docker is available and you have time, also lint:

```bash
docker run --rm --volume "$PWD:$PWD" --workdir="$PWD" \
  golangci/golangci-lint:v2.10.1 \
  golangci-lint run --timeout 5m ./<target>/...
```

Must report `0 issues.` If anything fails, **fix it before reporting success**. Do not use `//nolint` comments as a workaround.

## Pitfalls that burned the pilot

- **`SliceLen`/`MapLen` argument order is flipped from testify.** Always `(t, n, collection)`. After the rewrite pass, re-scan the file for these calls and double-check.
- **`Eq` vs `EqOp`.** `EqOp` on a slice/map is a compile error. Switch to `Eq` and try again.
- **`github.com/shoenig/test/test` does not exist.** The non-fatal package is the module root.
- **LSP diagnostics lag.** If the IDE reports errors that don't match your recent edits, trust `go build` instead — if build exits 0, the LSP is stale.
- **Don't touch `T` / `t` conventions.** Top-level test functions use `T *testing.T`; subtests use `t *testing.T`. Never rename.

## Report format

Post a concise summary (under 30 lines):

1. **Files modified** — list the `*_test.go` files touched.
2. **Counts** — approximate: `assert.* → test.*: N`, `require.* → must.*: M`.
3. **Any tricky calls** — list any assertions where you picked `Eq` over `EqOp` for a non-obvious reason, any `Panics`/`Greater`/`Less` calls you left alone, any calls you had to replace with stdlib.
4. **What WASN'T touched** — confirm mocks were left alone (testify mock imports still present if any were before).
5. **Verification results** — ✓ on each check in the list, or what failed.
6. **Git status summary** — short list of modified files (no diff).

Keep it tight. The user will invoke this across many packages and wants each report readable at a glance.

## Edge cases

- **Target has no testify assertions** — report "nothing to do, package already converted (or never used testify)" and exit.
- **Target has testify assertions AND testify mocks** — convert assertions only, leave mocks. The file becomes a hybrid. Explicitly mention this in the report so the user knows `/convert-mocks` is still needed.
- **Target has a shared helper file** (e.g., `helpers_test.go`) that uses testify assertions and is imported by tests in sub-packages — convert it, but flag in the report that it's a shared helper so the user can check sub-package tests compile.
- **A call site is ambiguous about whether `x` is a slice or a map** — read more context (declaration, surrounding ops like `append` or `[key]` indexing) to determine. If still unclear, flag it in the report with the file/line and leave the call untouched.

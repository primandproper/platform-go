package sentinelmatrix_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	httperrors "github.com/primandproper/platform-go/v14/errors/http"
	"github.com/primandproper/platform-go/v14/internal/sentinelmatrix"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
)

// TestEverySentinelHasADecision is the entry this package exists to make
// impossible to forget. A sentinel added to one of these packages and named in
// no row has had no decision made about it, which is the state where it reaches
// a client as a 500 on one transport and codes.Unknown on the other while every
// test in its own package stays green.
func TestEverySentinelHasADecision(T *testing.T) {
	T.Parallel()

	for _, pkg := range sentinelmatrix.Packages {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			declared := sentinelNames(t, pkg)
			must.SliceNotEmpty(t, declared, must.Sprintf("no sentinels parsed out of %s", pkg))

			for _, name := range declared {
				_, ok := sentinelmatrix.Matrix[pkg][name]
				test.True(t, ok, test.Sprintf(
					"%s.%s is a sentinel with no row here, so nothing says what a client is told when it happens", pkg, name))
			}
		})
	}
}

// TestNoRowOutlivesItsSentinel is the other direction, and the one a rename or a
// deletion breaks. A row naming a sentinel that is no longer there reads exactly
// like a live one, and a reader counting the mapped rows would be counting a
// mapping nothing produces.
func TestNoRowOutlivesItsSentinel(T *testing.T) {
	T.Parallel()

	for _, pkg := range sentinelmatrix.Packages {
		T.Run(pkg, func(t *testing.T) {
			t.Parallel()

			declared := sentinelNames(t, pkg)

			for name := range sentinelmatrix.Matrix[pkg] {
				test.True(t, slices.Contains(declared, name), test.Sprintf(
					"%s.%s has a row here and is not a sentinel in that package any more", pkg, name))
			}
		})
	}
}

// TestEveryDecisionHoldsOnBothTransports checks the rows against what the
// mappers actually do. A row is a claim about a client's experience, and a claim
// nothing verifies is how the gRPC mapper came to be missing sessions and
// operations in the first place: an expired session reached an HTTP client as a
// considered 401 and a gRPC client as codes.Unknown, and which one you got
// depended on how you had connected.
func TestEveryDecisionHoldsOnBothTransports(T *testing.T) {
	T.Parallel()

	for _, pkg := range sentinelmatrix.Packages {
		for name, row := range sentinelmatrix.Matrix[pkg] {
			T.Run(pkg+"."+name, func(t *testing.T) {
				t.Parallel()

				// Bare and wrapped, because a handler wraps: a mapping that only
				// works on the sentinel itself works nowhere real.
				assertDecision(t, pkg, name, row, row.Err)
				assertDecision(t, pkg, name, row, platformerrors.Wrap(row.Err, "doing the thing"))
			})
		}
	}
}

// assertDecision checks one row against both transports, through the package's
// own mappers and through the platform ones.
//
// It asks the mappers directly rather than through ToAPIError and MapToGRPC,
// which would answer out of a process-global registry: whether somebody has
// called RegisterHTTPErrorMapper is a property of a binary's wiring, and this
// package is about whether the mapping exists to be registered.
func assertDecision(t *testing.T, pkg, name string, row sentinelmatrix.Decision, err error) {
	t.Helper()

	httpDomain, grpcDomain := sentinelmatrix.Mappers(pkg)

	_, _, byDomainHTTP := httpDomain.Map(err)
	_, byDomainGRPC := grpcDomain.Map(err)

	_, _, byPlatformHTTP := httperrors.PlatformMapper.Map(err)
	byPlatformCode, byPlatformGRPC := grpcerrors.PlatformMapper.Map(err)

	switch row.Is {
	case sentinelmatrix.Mapped:
		test.True(t, byDomainHTTP, test.Sprintf("%s.%s is %v and %s.HTTPMapper does not answer it", pkg, name, row.Is, pkg))
		test.True(t, byDomainGRPC, test.Sprintf("%s.%s is %v and %s.GRPCMapper does not answer it", pkg, name, row.Is, pkg))
	case sentinelmatrix.Platform:
		test.False(t, byDomainHTTP, test.Sprintf("%s.%s is %v and %s.HTTPMapper claims it too", pkg, name, row.Is, pkg))
		test.False(t, byDomainGRPC, test.Sprintf("%s.%s is %v and %s.GRPCMapper claims it too", pkg, name, row.Is, pkg))
		test.True(t, byPlatformHTTP, test.Sprintf("%s.%s is %v and errors/http does not answer it", pkg, name, row.Is))
		test.True(t, byPlatformGRPC, test.Sprintf("%s.%s is %v and errors/grpc does not answer it", pkg, name, row.Is))
	case sentinelmatrix.Unhandled:
		test.False(t, byDomainHTTP, test.Sprintf("%s.%s is %v and %s.HTTPMapper answers it", pkg, name, row.Is, pkg))
		test.False(t, byDomainGRPC, test.Sprintf("%s.%s is %v and %s.GRPCMapper answers it", pkg, name, row.Is, pkg))
		test.False(t, byPlatformHTTP, test.Sprintf("%s.%s is %v and errors/http answers it", pkg, name, row.Is))
		test.False(t, byPlatformGRPC, test.Sprintf("%s.%s is %v and errors/grpc answers it", pkg, name, row.Is))
		test.EqOp(t, codes.Unknown, byPlatformCode)
	default:
		t.Fatalf("%s.%s carries no disposition", pkg, name)
	}
}

// sentinelNames is every exported name beginning with Err declared as a
// package-level var in pkg.
//
// The ground truth is deliberately crude, in the manner of
// internal/cmd/readmegen: a var whose name starts with Err is a sentinel. That
// finds one by how it is written rather than by anything it declares, which is
// the property that matters — a sentinel added in the ordinary way is precisely
// the one to catch.
var parsed = sync.OnceValues(func() (map[string][]string, error) {
	found := map[string][]string{}

	for _, pkg := range sentinelmatrix.Packages {
		dir := filepath.Join(moduleRootPath(), pkg)

		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}

		var names []string

		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}

			file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
			if parseErr != nil {
				return nil, parseErr
			}

			names = append(names, sentinelsIn(file)...)
		}

		slices.Sort(names)
		found[pkg] = names
	}

	return found, nil
})

func sentinelNames(t *testing.T, pkg string) []string {
	t.Helper()

	found, err := parsed()
	must.NoError(t, err)

	return found[pkg]
}

// sentinelsIn reads one file's package-level Err vars.
func sentinelsIn(file *ast.File) []string {
	var names []string

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue {
				continue
			}

			for _, ident := range value.Names {
				if strings.HasPrefix(ident.Name, "Err") && ident.IsExported() {
					names = append(names, ident.Name)
				}
			}
		}
	}

	return names
}

// TestErrorsDoesNotImportTheTierAboveIt is the invariant the mappings were moved
// to establish, and the one thing here the compiler does not already enforce.
//
// A non-test file under errors/ that imported one of these four would be an
// import cycle and would not build, so it needs no test. An external test
// package — errors' own mapper_parity_test.go is one — can import them freely,
// which is how the dependency would come back: a test reaching for a domain
// sentinel to assert something about, and errors/ quietly stops being a package
// that can be lifted out on its own.
func TestErrorsDoesNotImportTheTierAboveIt(T *testing.T) {
	T.Parallel()

	root := filepath.Join(moduleRootPath(), "errors")

	var checked int

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return walkErr
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}

		checked++

		rel, relErr := filepath.Rel(moduleRootPath(), path)
		if relErr != nil {
			return relErr
		}

		for _, imported := range file.Imports {
			for _, pkg := range sentinelmatrix.Packages {
				test.False(T, strings.HasSuffix(strings.Trim(imported.Path.Value, `"`), "/"+pkg), test.Sprintf(
					"%s imports %s, which imports errors/http and errors/grpc — the mappings were moved out of errors/ so that it depends on nothing above it", rel, pkg))
			}
		}

		return nil
	})
	must.NoError(T, err)

	test.Greater(T, 0, checked, test.Sprint("no files parsed under errors/, so this test asserted nothing"))
}

// TestModuleRootIsThisModule keeps the walk above honest. A test binary run from
// anywhere but this package's directory would read four directories that are not
// these, or none, and a roster that matches nothing would report as a roster that
// matches everything — except that TestEverySentinelHasADecision insists on a
// non-empty parse, which is the other half of the same guard.
func TestModuleRootIsThisModule(T *testing.T) {
	T.Parallel()

	must.FileExists(T, filepath.Join(moduleRootPath(), "go.mod"))

	for _, pkg := range sentinelmatrix.Packages {
		must.DirExists(T, filepath.Join(moduleRootPath(), pkg))
	}
}

// moduleRootPath is two directories up, which is where this package sits and
// where go.mod has to be for the answer to be this module rather than whatever
// tree a test binary was copied into.
var moduleRootPath = sync.OnceValue(func() string {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		panic(err)
	}

	return root
})

// TestMappedResolutionsIsEveryMappedRow is the roster's own check on the list it
// hands the two registration tests. They assert that a registered mapper answers
// what this says, which asserts nothing at all if a row is missing from it.
func TestMappedResolutionsIsEveryMappedRow(T *testing.T) {
	T.Parallel()

	resolutions := sentinelmatrix.MappedResolutions()
	must.SliceNotEmpty(T, resolutions)

	expected := 0

	for _, pkg := range sentinelmatrix.Packages {
		for _, row := range sentinelmatrix.Matrix[pkg] {
			if row.Is == sentinelmatrix.Mapped {
				expected++
			}
		}
	}

	test.SliceLen(T, expected, resolutions)

	// Every entry carries the answer its own package's mappers give, which is
	// what the registration tests compare against. A row with no code on either
	// transport would pass those tests by asserting nothing.
	for _, resolution := range resolutions {
		T.Run(resolution.Package+"."+resolution.Name, func(t *testing.T) {
			t.Parallel()

			test.NotEq(t, httperrors.ErrorCode(""), resolution.HTTPCode)
			test.NotEq(t, "", resolution.HTTPMsg)
			test.NotEqOp(t, codes.Unknown, resolution.GRPCCode)
			test.Error(t, resolution.Err)
		})
	}

	// The order is Packages and then name, so a failure names the same row twice
	// in a row rather than a different one each run.
	sorted := slices.IsSortedFunc(resolutions, func(a, b sentinelmatrix.Resolution) int {
		if a.Package != b.Package {
			return slices.Index(sentinelmatrix.Packages, a.Package) -
				slices.Index(sentinelmatrix.Packages, b.Package)
		}

		return strings.Compare(a.Name, b.Name)
	})
	test.True(T, sorted, test.Sprint("MappedResolutions is unordered, so a failure names a different row each run"))
}

// TestEveryDispositionRendersItself: the roster's failure messages are built out
// of these, and a disposition added later that rendered as "unknown" would make
// the message that reports it useless at exactly the moment somebody needs it.
func TestEveryDispositionRendersItself(T *testing.T) {
	T.Parallel()

	seen := map[string]struct{}{}

	for _, disposition := range []sentinelmatrix.Disposition{
		sentinelmatrix.Mapped, sentinelmatrix.Platform, sentinelmatrix.Unhandled,
	} {
		rendered := disposition.String()
		test.NotEq(T, "unknown", rendered,
			test.Sprintf("disposition %d renders as the fallback", int(disposition)))
		seen[rendered] = struct{}{}
	}

	test.MapLen(T, 3, seen, test.Sprint("two dispositions render the same, so a message cannot tell them apart"))

	// The fallback itself, for a value no constant names — which is what a
	// disposition added without a String case would be.
	test.EqOp(T, "unknown", sentinelmatrix.Disposition(99).String())
}

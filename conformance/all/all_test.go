package all

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestEverySuiteIsRegistered keeps Suites from being a list that quietly stops
// describing the tree.
//
// A suite is library code, so `go test` reports its package as "[no test
// files]" whether or not anything ever calls it. A suite missing from Suites
// therefore compiles, passes and asserts nothing, and nothing else in the run
// would say so. This finds every package under conformance/ that declares a
// Suite function and requires it here — named, as every suite is, after its
// directory — and the reverse, so an entry cannot outlive its package.
func TestEverySuiteIsRegistered(t *testing.T) {
	t.Parallel()

	declared := suitePackages(t)
	must.MapNotEmpty(t, declared, must.Sprint("no package under conformance/ declares a Suite; this test would assert nothing"))

	registered := map[string]bool{}
	for _, suite := range Suites() {
		registered[suite.Name] = true
	}

	for dir := range declared {
		test.MapContainsKey(t, registered, dir, test.Sprintf(
			"conformance/%s declares a Suite that Suites does not return, so no subject runs it; "+
				"add it there, named after its directory", dir))
	}

	for name := range registered {
		test.MapContainsKey(t, declared, name, test.Sprintf(
			"Suites returns %q, which is not the name of a conformance/ package declaring a Suite; "+
				"a suite is named after its directory", name))
	}
}

// suitePackages is every directory directly under conformance/ whose non-test
// Go files declare `func Suite()`.
func suitePackages(t *testing.T) map[string]bool {
	t.Helper()

	root, err := filepath.Abs("..")
	must.NoError(t, err)

	entries, err := os.ReadDir(root)
	must.NoError(t, err)

	out := map[string]bool{}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		files, globErr := filepath.Glob(filepath.Join(root, entry.Name(), "*.go"))
		must.NoError(t, globErr)

		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}

			parsed, parseErr := parser.ParseFile(token.NewFileSet(), file, nil, parser.SkipObjectResolution)
			must.NoError(t, parseErr)

			for _, decl := range parsed.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if ok && fn.Recv == nil && fn.Name.Name == "Suite" && fn.Type.Params.NumFields() == 0 {
					out[entry.Name()] = true
				}
			}
		}
	}

	return out
}

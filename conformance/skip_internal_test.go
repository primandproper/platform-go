package conformance

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestSkips(t *testing.T) {
	t.Parallel()

	t.Run("reports each skip beneath the test, named relative to it", func(t *testing.T) {
		t.Parallel()

		t.Run("run", func(t *testing.T) {
			t.Parallel()

			// The subtests are parallel, so they finish after this function
			// returns; a cleanup is what runs once they have, which is how a
			// harness asks too.
			t.Cleanup(func() {
				got := Skips(t)
				slices.SortFunc(got, func(a, b Skipped) int { return strings.Compare(a.Test, b.Test) })

				test.Eq(t, []Skipped{
					{Test: "nested/deeper", Reason: "plainly"},
					{Test: "skipped", Reason: "because reasons"},
				}, got)
			})

			t.Run("skipped", func(t *testing.T) {
				t.Parallel()
				Skipf(t, "because %s", "reasons")
			})

			t.Run("nested", func(t *testing.T) {
				t.Parallel()
				t.Run("deeper", func(t *testing.T) {
					t.Parallel()
					Skip(t, "plainly")
				})
			})

			t.Run("ran", func(t *testing.T) { t.Parallel() })
		})
	})

	t.Run("reports nothing from a sibling whose name it prefixes", func(t *testing.T) {
		t.Parallel()

		record("TestSkipsSibling/apart/x", "the sibling's")
		record("TestSkipsSibling/apar/y", "its own")

		test.Eq(t, []Skipped{{Test: "y", Reason: "its own"}}, skipsUnder("TestSkipsSibling/apar"))
	})
}

// TestSuitesSkipThroughSkip holds every suite to recording its skips: a skip
// made with t.Skip is one no harness can see, and a harness that cannot see a
// skip cannot tell a deployment's absence from a wiring regression.
func TestSuitesSkipThroughSkip(t *testing.T) {
	t.Parallel()

	var offenders []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || path == "skip.go" {
			return err
		}

		fset := token.NewFileSet()

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}

			// Skip and Skipf are what the helpers call; a package-qualified
			// conformance.Skip is the call this test asks for.
			if name := sel.Sel.Name; name == "Skip" || name == "Skipf" || name == "SkipNow" {
				if ident, isIdent := sel.X.(*ast.Ident); !isIdent || ident.Name != "conformance" {
					offenders = append(offenders, fset.Position(sel.Pos()).String())
				}
			}

			return true
		})

		return nil
	})
	must.NoError(t, err)

	test.SliceEmpty(t, offenders, test.Sprintf(
		"a suite skipped with testing's own Skip rather than conformance.Skip or Skipf at %s", strings.Join(offenders, ", ")))
}

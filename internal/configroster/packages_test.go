package configroster_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// leafConfig names a config subpackage that declares no Config of its own, and
// the package whose Config the roster zero-values in its place.
type leafConfig struct {
	// covers is the module-relative package the roster's case points at, as a
	// reflected package path spells it: "workqueue", not "workqueue/config".
	covers string
	// why is the reason there is no wrapper to zero-value.
	why string
}

// configPackagesTakingALeafConfig are the config subpackages in this module
// that declare no Config of their own, each naming the config the roster covers
// instead.
//
// An entry is a decision, not an exemption. A config subpackage that acquires a
// Config of its own is deleted from this map — the test below fails on a stale
// entry rather than quietly reading past it — and a new one that declines to
// declare one has to be argued for in a line of prose before the suite goes
// green, which is the point. workqueuecfg's package documentation is the
// argument for the only entry here.
var configPackagesTakingALeafConfig = map[string]leafConfig{
	"workqueue/config": {
		covers: "workqueue",
		why:    "a wrapper would hold one workqueue.Config field, forward two methods workqueue.New already calls, and nest every variable one level deeper for it",
	},
}

// TestEveryConfigPackageIsRostered asserts that every config subpackage in this
// module is named by a case in zeroValueCases.
//
// It reads the tree rather than a list, because a list was what the roster's own
// comment already amounted to: "adding a config subpackage means adding a line
// here" was true and enforced by nothing, and by the time it was read the roster
// named fourteen of this module's twenty-five. The eleven it had lost were the
// newest ones, which is the direction that omission always runs — a roster is
// complete on the day it is written and never again.
//
// It is the same shape as service's TestEveryConfigPackageHasAField and for the
// same reason: the two rosters this module keeps about its own config packages
// are both checked against the directories, so neither can be right about a
// module that has moved on.
func TestEveryConfigPackageIsRostered(t *testing.T) {
	t.Parallel()

	rostered := rosteredConfigPackages(t)
	found := configPackages(t)

	for pkg, declaresConfig := range found {
		leaf, takesALeafConfig := configPackagesTakingALeafConfig[pkg]

		if !declaresConfig {
			test.True(t, takesALeafConfig,
				test.Sprintf("%s declares no Config of its own and says nowhere which config a deployment sets instead, "+
					"so nothing holds that one to the zero-value rule", pkg))

			if takesALeafConfig {
				test.True(t, rostered[leaf.covers],
					test.Sprintf("%s takes %s's Config (%s), and the roster does not zero-value that one either",
						pkg, leaf.covers, leaf.why))
			}

			continue
		}

		test.False(t, takesALeafConfig,
			test.Sprintf("%s declares a Config of its own and is still listed as taking a leaf's (%s); delete the roster entry", pkg, leaf.why))

		test.True(t, rostered[pkg],
			test.Sprintf("%s is a config subpackage no case in zeroValueCases names, "+
				"so nothing says whether its zero Config defaults into validity or reports the field it needs", pkg))
	}

	for pkg := range configPackagesTakingALeafConfig {
		_, exists := found[pkg]
		test.True(t, exists, test.Sprintf("%s is listed as taking a leaf's Config but is not a directory in this module; delete the roster entry", pkg))
	}
}

// rosteredConfigPackages returns the module-relative directory of every config
// the roster zero-values, so "audit/config" rather than the full import path.
//
// Cases whose configs come from primitives-go are skipped. That module's
// coverage is checked from the other side, by the packages' own tests and by
// whoever adds one; what this module can hold itself to is its own tree.
func rosteredConfigPackages(t *testing.T) map[string]bool {
	t.Helper()

	paths := map[string]bool{}

	// Indexed rather than ranged by value: a zeroValueCase is four words, and
	// only the config's type is wanted here.
	cases := zeroValueCases()
	for i := range cases {
		pkgPath := reflect.TypeOf(cases[i].cfg).Elem().PkgPath()
		if rel, ok := strings.CutPrefix(pkgPath, platformModule); ok {
			paths[rel] = true
		}
	}

	must.MapNotEmpty(t, paths, must.Sprint("the roster names no platform-go config at all; the reflection, not the roster, is what broke"))

	return paths
}

// configPackages returns every directory in this module named config, mapped to
// whether it declares a Config type of its own.
//
// The directory name is the whole rule. Every config subpackage in the module is
// one, the convention is what the CLAUDE.md calls it, and a heuristic over
// declared types would find the leaf configs — workqueue.Config, saga.Config,
// webhooks.WorkerConfig — that are nested inside these rather than set beside
// them.
func configPackages(t *testing.T) map[string]bool {
	t.Helper()

	root, err := filepath.Abs("../..")
	must.NoError(t, err)

	packages := map[string]bool{}

	must.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if !entry.IsDir() {
			return nil
		}

		// Neither holds a config package, and both hold enough files to be
		// worth not walking.
		if name := entry.Name(); name == ".git" || name == ".claude" {
			return fs.SkipDir
		}

		if entry.Name() != "config" {
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		packages[filepath.ToSlash(rel)] = directoryDeclaresConfig(t, path)

		return nil
	}))

	must.MapNotEmpty(t, packages, must.Sprint("found no config subpackages anywhere in the module; the walk, not the tree, is what broke"))

	return packages
}

// directoryDeclaresConfig reports whether the non-test Go files in dir declare
// an exported type named Config.
func directoryDeclaresConfig(t *testing.T, dir string) bool {
	t.Helper()

	entries, err := os.ReadDir(dir)
	must.NoError(t, err)

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, entry.Name()), nil, parser.SkipObjectResolution)
		must.NoError(t, parseErr)

		for _, decl := range file.Decls {
			genDecl, isGen := decl.(*ast.GenDecl)
			if !isGen || genDecl.Tok != token.TYPE {
				continue
			}

			for _, spec := range genDecl.Specs {
				if typeSpec, isType := spec.(*ast.TypeSpec); isType && typeSpec.Name.Name == "Config" {
					return true
				}
			}
		}
	}

	return false
}

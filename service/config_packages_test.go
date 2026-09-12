package service

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

// thisModule is the import path Config's field types carry for packages in this
// repository. It is spelled out rather than derived because deriving it from a
// field's own PkgPath would make the check agree with whatever the fields
// happen to say.
const thisModule = "github.com/primandproper/platform-go/v14"

// exemptConfigPackages are the config packages in this module that deliberately
// have no field on Config, each with the reason it has none.
//
// It is a roster rather than a rule because "generic" is not a property a test
// can read off a directory: what makes these three different is that their
// Register functions take a type argument, so one field could only switch on one
// instantiation of them, and which instantiations a service wants is a fact
// about the service's own types. The alternative — a Config field per concrete
// type somebody might want a queue of — is not a config, so these stay explicit
// calls on the injector Register does not hide.
//
// An entry here is a decision. A package that acquires a field is deleted from
// this map, and a package added to the module with no field has to be argued for
// in a line of prose before this test goes green, which is the point.
var exemptConfigPackages = map[string]string{
	"sessions/config":  "sessions.Manager[T] is registered per session payload type",
	"timers/config":    "timers.Timers[T] is registered per timer payload type",
	"workqueue/config": "workqueue.Queue[T] is registered per queued message type",
}

// TestEveryConfigPackageHasAField asserts that every config package in this
// module shipping a Register bridge is named by a field on Config, or is in the
// exempt roster above.
//
// It is the direction TestRegister's "every sub-config field registers
// something" cannot see. That one starts from the struct and catches a field
// nothing reads; this one starts from the tree and catches a package nothing can
// reach — which is the failure that actually happened, six times, and stayed
// invisible because a Register function nobody calls compiles exactly as well as
// one everybody does. The symptom is not a broken build but a domain a
// deployment cannot switch on without assembling the sub-config by hand, which
// is the composition root failing at the one thing it is for.
//
// It reads the tree rather than a list, because a list is what the package
// documentation already was.
func TestEveryConfigPackageHasAField(t *testing.T) {
	t.Parallel()

	fielded := configPackagePaths(t)

	for _, pkg := range configPackagesWithBridges(t) {
		if reason, exempt := exemptConfigPackages[pkg]; exempt {
			test.False(t, fielded[pkg],
				test.Sprintf("%s has a field on Config and is still listed as exempt (%s); delete the roster entry", pkg, reason))

			continue
		}

		test.True(t, fielded[pkg],
			test.Sprintf("%s registers something with do but no Config field switches it on, "+
				"so a deployment can only reach it by assembling the sub-config by hand", pkg))
	}
}

// configPackagePaths returns the module-relative directory of every config
// package a Config field points at, so "audit/config" rather than the full
// import path.
//
// Fields whose types come from primitives-go are skipped. That module's
// coverage is checked from the other side, by the packages' own tests and by
// whoever adds one; what this module can hold itself to is its own tree.
func configPackagePaths(t *testing.T) map[string]bool {
	t.Helper()

	paths := map[string]bool{}

	for field := range reflect.TypeFor[Config]().Fields() {
		if !field.IsExported() || field.Type.Kind() != reflect.Pointer {
			continue
		}

		pkgPath := field.Type.Elem().PkgPath()
		if rel, ok := strings.CutPrefix(pkgPath, thisModule+"/"); ok {
			paths[rel] = true
		}
	}

	must.MapNotEmpty(t, paths, must.Sprint("found no platform-go sub-config fields; the reflection, not the config, is what broke"))

	return paths
}

// configPackagesWithBridges returns the module-relative directory of every
// package in this repository declaring an exported function whose name starts
// with Register and whose only parameter is a do.Injector.
//
// That signature is the definition of a bridge rather than a heuristic: it is
// the shape every Register* in both modules has. A constructor taking a Config
// and dependencies is not one, and neither is a package's own RegisterFeature or
// RegisterPlan.
//
// A type parameter does not disqualify one. The generic bridges are the whole
// reason the exempt roster exists, so excluding them by signature would answer
// the question this test asks by never asking it of the three packages it
// matters for.
func configPackagesWithBridges(t *testing.T) []string {
	t.Helper()

	root, err := filepath.Abs("..")
	must.NoError(t, err)

	var packages []string

	must.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			// Neither holds a config package, and both hold enough files to be
			// worth not parsing.
			if name := entry.Name(); name == ".git" || name == ".claude" {
				return fs.SkipDir
			}

			return nil
		}

		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		if !fileDeclaresInjectorBridge(t, path) {
			return nil
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}

		packages = append(packages, filepath.ToSlash(rel))

		return nil
	}))

	must.SliceNotEmpty(t, packages, must.Sprint("found no Register bridges anywhere in the module; the walk, not the tree, is what broke"))

	return packages
}

// fileDeclaresInjectorBridge reports whether path declares an exported
// Register* function taking exactly one do.Injector.
func fileDeclaresInjectorBridge(t *testing.T, path string) bool {
	t.Helper()

	source, err := os.ReadFile(path)
	must.NoError(t, err)

	// A cheap reject before the parse, because most of the tree is neither.
	if !strings.Contains(string(source), "do.Injector") {
		return false
	}

	file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	must.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Register") || !fn.Name.IsExported() {
			continue
		}

		if len(fn.Type.Params.List) != 1 {
			continue
		}

		if sel, isSel := fn.Type.Params.List[0].Type.(*ast.SelectorExpr); isSel && sel.Sel.Name == "Injector" {
			return true
		}
	}

	return false
}

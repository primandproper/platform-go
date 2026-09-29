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

// exemptionKind is why a config package has no field on Config.
type exemptionKind int

const (
	// generic is a package whose Register bridge takes a type argument, so one
	// field could only switch on one instantiation of it, and which
	// instantiations a service wants is a fact about the service's own types.
	// The test checks the bridge really is generic.
	generic exemptionKind = iota + 1

	// standalone is a package meant to be composed by its caller rather than
	// switched on by a service's Config: it is complete on its own, and
	// mounting it from Config would make service carry a sub-config for a
	// domain only some deployments compose. Nothing about the tree can say
	// so, which is why the reason beside it is required.
	standalone
)

// exemption is one roster entry: the kind of exemption, and the reason in
// prose.
type exemption struct {
	reason string
	kind   exemptionKind
}

// exemptConfigPackages are the config packages in this module that deliberately
// have no field on Config, each with the reason it has none.
//
// It is a roster rather than a rule because neither kind is fully a property a
// test can read off a directory. A generic bridge is checked for its type
// parameter, but the decision that a Config field per concrete type somebody
// might want a queue of is not a config is still this roster's. A standalone
// package is one whose own caller composes it — Config not reaching it is the
// design rather than the gap this test exists to catch — and only its reason
// says so.
//
// An entry here is a decision. A package that acquires a field is deleted from
// this map, and a package added to the module with no field has to be argued for
// in a line of prose before this test goes green, which is the point.
var exemptConfigPackages = map[string]exemption{
	"sessions/config":  {kind: generic, reason: "sessions.Manager[T] is registered per session payload type"},
	"timers/config":    {kind: generic, reason: "timers.Timers[T] is registered per timer payload type"},
	"workqueue/config": {kind: generic, reason: "workqueue.Queue[T] is registered per queued message type"},
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
	bridges := configPackagesWithBridges(t)

	for pkg, bridge := range bridges {
		if err := judgeConfigPackage(pkg, fielded[pkg], bridge, exemptConfigPackages); err != "" {
			t.Error(err)
		}
	}

	for pkg := range exemptConfigPackages {
		_, found := bridges[pkg]
		test.True(t, found, test.Sprintf("%s is listed as exempt and declares no Register bridge; delete the roster entry", pkg))
	}
}

// judgeConfigPackage is TestEveryConfigPackageHasAField's ruling on one
// package, as the failure it would report or "" for none. It is a function of
// its arguments, so the roster's rules are testable against rosters other than
// the one this module ships.
func judgeConfigPackage(pkg string, fielded bool, bridge bridgeShape, roster map[string]exemption) string {
	entry, exempt := roster[pkg]
	if !exempt {
		if !fielded {
			return pkg + " registers something with do but no Config field switches it on, " +
				"so a deployment can only reach it by assembling the sub-config by hand; " +
				"give it a field, or list it in exemptConfigPackages with its reason"
		}

		return ""
	}

	switch {
	case fielded:
		return pkg + " has a field on Config and is still listed as exempt (" + entry.reason + "); delete the roster entry"
	case strings.TrimSpace(entry.reason) == "":
		return pkg + " is listed as exempt with no reason; an exemption is a decision, and its reason is the record of it"
	case entry.kind == generic && !bridge.generic:
		return pkg + " is exempt as generic and its Register bridge takes no type argument; " +
			"give it a field, or list it as standalone with its reason"
	case entry.kind != generic && entry.kind != standalone:
		return pkg + " is listed as exempt with no kind"
	}

	return ""
}

func TestJudgeConfigPackage(T *testing.T) {
	T.Parallel()

	roster := map[string]exemption{
		"queue/config":      {kind: generic, reason: "Queue[T] is registered per message type"},
		"composed/config":   {kind: standalone, reason: "composed by the service that owns its surface, never by Config"},
		"unreasoned/config": {kind: standalone, reason: " "},
	}

	T.Run("a package with a field needs no exemption", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", judgeConfigPackage("fielded/config", true, bridgeShape{}, roster))
	})

	T.Run("a package with neither a field nor an exemption fails", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, judgeConfigPackage("orphan/config", false, bridgeShape{}, roster), "no Config field")
	})

	T.Run("a generic exemption over a generic bridge passes", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", judgeConfigPackage("queue/config", false, bridgeShape{generic: true}, roster))
	})

	T.Run("a generic exemption over a bridge with no type argument fails", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, judgeConfigPackage("queue/config", false, bridgeShape{}, roster), "takes no type argument")
	})

	T.Run("a standalone package with its reason stated passes", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", judgeConfigPackage("composed/config", false, bridgeShape{}, roster))
	})

	T.Run("an exemption with no reason fails", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, judgeConfigPackage("unreasoned/config", false, bridgeShape{}, roster), "no reason")
	})

	T.Run("an exempt package that has acquired a field fails", func(t *testing.T) {
		t.Parallel()

		test.StrContains(t, judgeConfigPackage("composed/config", true, bridgeShape{}, roster), "delete the roster entry")
	})
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

// bridgeShape is what a package's Register bridges look like, as far as the
// roster cares.
type bridgeShape struct {
	// generic is whether any of them takes a type argument.
	generic bool
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
func configPackagesWithBridges(t *testing.T) map[string]bridgeShape {
	t.Helper()

	root, err := filepath.Abs("..")
	must.NoError(t, err)

	packages := map[string]bridgeShape{}

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

		found, isGeneric := fileDeclaresInjectorBridge(t, path)
		if !found {
			return nil
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}

		pkg := filepath.ToSlash(rel)
		packages[pkg] = bridgeShape{generic: packages[pkg].generic || isGeneric}

		return nil
	}))

	must.MapNotEmpty(t, packages, must.Sprint("found no Register bridges anywhere in the module; the walk, not the tree, is what broke"))

	return packages
}

// fileDeclaresInjectorBridge reports whether path declares an exported
// Register* function taking exactly one do.Injector, and whether one that it
// declares takes a type argument.
func fileDeclaresInjectorBridge(t *testing.T, path string) (found, generic bool) {
	t.Helper()

	source, err := os.ReadFile(path)
	must.NoError(t, err)

	// A cheap reject before the parse, because most of the tree is neither.
	if !strings.Contains(string(source), "do.Injector") {
		return false, false
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
			found = true
			generic = generic || (fn.Type.TypeParams != nil && len(fn.Type.TypeParams.List) > 0)
		}
	}

	return found, generic
}

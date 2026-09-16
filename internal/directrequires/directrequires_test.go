package directrequires_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// requireEntry matches one entry of a require block: a module path, a version,
// and whatever comment the line carries.
var requireEntry = regexp.MustCompile(`^(\S+)\s+(v\S+)(\s*//.*)?$`)

// indirectClaim matches the claim this package exists to check, on the word
// rather than on the whole comment: what follows the marker is free text, and a
// line in this module's go.mod carries some.
var indirectClaim = regexp.MustCompile(`//\s*indirect\b`)

// TestEveryDirectRequireIsImported is the direction that has gone wrong here.
//
// A require outside the indirect block says some file in this module imports
// that module. Nothing rechecks it when the file that did is deleted, moved to
// another repository, or rewritten against something else, so the line survives
// as a version this module looks like it is choosing and no longer is.
func TestEveryDirectRequireIsImported(T *testing.T) {
	T.Parallel()

	requires := requirements(T)
	importers := importersByModule(T)

	checked := 0

	for _, path := range slices.Sorted(maps.Keys(requires)) {
		if requires[path] {
			continue
		}

		checked++

		_, imported := importers[path]
		test.True(T, imported, test.Sprintf(
			"go.mod requires %s directly and no file in this module imports it. Either the "+
				"package that did has left and took the reason with it, or the require was never "+
				"this module's to make: run `go mod tidy` and commit go.mod", path))
	}

	must.Positive(T, checked, must.Sprint(
		"no require in go.mod parsed as direct, so this test asserted nothing"))
}

// TestEveryIndirectRequireIsUnimported is the same drift arriving from the other
// side, and the worse one.
//
// A module this repository's own source names is a module this repository has a
// version opinion about. Left marked indirect, the version is whatever some
// other module in the graph asks for, and the require line disappears from
// go.mod on the day that module stops asking — taking the pin with it while the
// import stays behind.
func TestEveryIndirectRequireIsUnimported(T *testing.T) {
	T.Parallel()

	requires := requirements(T)
	importers := importersByModule(T)

	checked := 0

	for _, path := range slices.Sorted(maps.Keys(requires)) {
		if !requires[path] {
			continue
		}

		checked++

		importer, imported := importers[path]
		test.False(T, imported, test.Sprintf(
			"go.mod marks %s indirect and %s imports it. A module this one's own source names is "+
				"a module it pins: run `go mod tidy` and commit go.mod", path, importer))
	}

	must.Positive(T, checked, must.Sprint(
		"no require in go.mod parsed as indirect, so this test asserted nothing"))
}

// parsedRequires is go.mod's require lines, keyed by module path and holding
// go.mod's own claim about each: true where the line says `// indirect`.
//
// Read once. Both tests want it, and it is a property of the file rather than of
// whichever test asked first.
var parsedRequires = sync.OnceValues(func() (map[string]bool, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}

	return parseRequires(root)
})

// scannedImports is every import path reached for by a .go file in this module,
// mapped to the first such file in walk order — enough for a failure to point
// somewhere, and deterministic because the walk is lexical.
var scannedImports = sync.OnceValues(func() (map[string]string, error) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		return nil, err
	}

	return scanImports(root)
})

func requirements(t *testing.T) map[string]bool {
	t.Helper()

	requires, err := parsedRequires()
	must.NoError(t, err)
	must.Positive(t, len(requires), must.Sprint("go.mod holds no require lines"))

	return requires
}

// importersByModule answers, for each require in go.mod, a file in this module
// that imports it, or nothing where none does.
//
// The resolution is longest matching path prefix, because that is how an import
// path and a module path relate: go.opentelemetry.io/otel/metric is its own
// module with its own require line, so an import of it must answer for that line
// and not for go.opentelemetry.io/otel's.
func importersByModule(t *testing.T) map[string]string {
	t.Helper()

	imports, err := scannedImports()
	must.NoError(t, err)
	must.Positive(t, len(imports), must.Sprint("no .go file in this module imports anything"))

	requires := requirements(t)
	importers := make(map[string]string, len(requires))

	for _, imported := range slices.Sorted(maps.Keys(imports)) {
		owner := ""

		for path := range requires {
			if imported != path && !strings.HasPrefix(imported, path+"/") {
				continue
			}

			if len(path) > len(owner) {
				owner = path
			}
		}

		if owner == "" {
			continue
		}

		if _, seen := importers[owner]; !seen {
			importers[owner] = imports[imported]
		}
	}

	return importers
}

// parseRequires reads go.mod itself rather than asking the go command, so that
// what is being checked is the file a reader and a reviewer see. The module
// graph is the workflow's business; the claim written down here is this one's.
func parseRequires(root string) (map[string]bool, error) {
	contents, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}

	requires := map[string]bool{}
	inBlock := false

	for number, line := range strings.Split(string(contents), "\n") {
		text := strings.TrimSpace(line)

		switch {
		case !inBlock && text == "require (":
			inBlock = true

			continue
		case !inBlock && strings.HasPrefix(text, "require "):
			text = strings.TrimSpace(strings.TrimPrefix(text, "require "))
		case inBlock && text == ")":
			inBlock = false

			continue
		case !inBlock:
			continue
		}

		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}

		entry := requireEntry.FindStringSubmatch(text)
		if entry == nil {
			return nil, fmt.Errorf("go.mod:%d: %q is a require and does not parse as one", number+1, text)
		}

		requires[entry[1]] = indirectClaim.MatchString(entry[3])
	}

	return requires, nil
}

// scanImports walks the tree for import paths.
//
// Test files are included and build constraints are not evaluated, which is
// close to the set `go mod tidy` considers: tidy loads every build configuration
// too, and the one file it would leave out that this walk takes in is one no
// configuration builds.
func scanImports(root string) (map[string]string, error) {
	imports := map[string]string{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// The skips sqltier's walk makes, for the reasons it makes them:
			// dot directories hold no packages of this module's, and one of
			// them holds other checkouts of it.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		for _, spec := range file.Imports {
			value, unquoteErr := strconv.Unquote(spec.Path.Value)
			if unquoteErr != nil {
				continue
			}

			if _, seen := imports[value]; !seen {
				imports[value] = filepath.ToSlash(rel)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return imports, nil
}

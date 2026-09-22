package callers_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the reason this package exists, executed. The three names in it
// used to be declared in identity/grpc, and every gRPC surface in the module
// that needed them linked the directory, its generated querier, its migrations
// and its protobuf bindings in order to compile an interface with three methods
// on it. Moving them here removed that edge, and an edge removed by a move is an
// edge a later import can quietly put back — so both halves of the property are
// pinned here rather than described.
//
// The walk is over this module's own source and treats everything outside it as
// a leaf, because what is being checked is which of *these* packages reach which
// others. It is the same shape internal/tiercheck's roster tests use, and it is
// here rather than there because the subject is what this package buys the
// surfaces, which is what a reader of this package came to find out.

// identityKeepers are the packages that reach identity on purpose, with the
// reason. Everything else in the module that ships a gRPC surface must not.
//
// It is a closed list: a surface that grows a legitimate identity dependency
// adds itself here with its reason, and one that grows an accidental one fails.
// The match is by longest directory prefix, so a surface's typed client
// inherits the surface's answer.
var identityKeepers = map[string]string{
	"identity":                   "identity's own package",
	"authentication/signin/grpc": "renders a signed-in user with identitygrpc.UserToProto",

	// A second kind of reason, and the distinction is worth keeping: this surface
	// names nothing of identity's itself. It inherits the edge from the service
	// it wraps, whose Directory seam is typed on *identity.User because a reset
	// reads a user by address and writes that user's hash. Anybody mounting this
	// has already linked the directory by constructing the service, so the
	// surface adds no edge a consumer could otherwise have avoided.
	"authentication/passwordreset/grpc": "inherits passwordreset.Service's Directory seam, which is typed on *identity.User",
}

// TestCallersIsALeaf pins the half a reader of this package can check without
// leaving it: nothing here imports anything else in this module. A single
// in-module import would make every surface that takes a principal link whatever
// that package reaches, which is the whole of what this package was carved out
// to stop.
func TestCallersIsALeaf(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)
	module := modulePath(T, root)

	imports := inModuleImports(T, root, module)

	test.SliceEmpty(T, imports["callers"],
		test.Sprintf("callers must import nothing else in this module; it imports %v", imports["callers"]))
}

// TestNoGRPCSurfaceLinksIdentity is the half that is about everybody else, and
// it is the acceptance criterion of the change that created this package: a
// consumer wiring only settings, or only comments, links neither the directory
// nor its generated code.
//
// Reachability rather than direct imports, because the edge that was removed was
// never direct in the packages that paid for it — settings/grpc named
// identity/grpc, which named identity, which named its querier and its
// migrations.
func TestNoGRPCSurfaceLinksIdentity(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)
	module := modulePath(T, root)

	imports := inModuleImports(T, root, module)

	surfaces := 0

	for _, dir := range slices.Sorted(maps.Keys(imports)) {
		if !strings.Contains(dir+"/", "/grpc/") && !strings.HasSuffix(dir, "/grpc") {
			continue
		}

		if _, ok := keeperFor(dir); ok {
			continue
		}

		surfaces++

		for _, reached := range slices.Sorted(maps.Keys(reachable(imports, dir))) {
			if reached == "identity" || strings.HasPrefix(reached, "identity/") {
				T.Errorf("%s reaches %s\n\tno gRPC surface here should link the directory; "+
					"who is calling is callers.Principal, and a surface with a real "+
					"identity dependency joins identityKeepers with its reason", dir, reached)
			}
		}
	}

	must.Positive(T, surfaces, must.Sprint("no gRPC surfaces walked, so this test asserted nothing"))
}

// keeperFor resolves a directory against identityKeepers by longest matching
// prefix, so a surface's typed client inherits the surface's answer where one
// was given for it.
func keeperFor(dir string) (string, bool) {
	for {
		if why, ok := identityKeepers[dir]; ok {
			return why, true
		}

		parent := filepath.ToSlash(filepath.Dir(dir))
		if parent == dir || parent == "." || parent == "/" {
			return "", false
		}

		dir = parent
	}
}

// reachable is the transitive closure of dir's in-module imports.
func reachable(imports map[string][]string, dir string) map[string]struct{} {
	seen := map[string]struct{}{}

	var walk func(string)
	walk = func(d string) {
		for _, next := range imports[d] {
			if _, ok := seen[next]; ok {
				continue
			}

			seen[next] = struct{}{}

			walk(next)
		}
	}

	walk(dir)

	return seen
}

// inModuleImports is every package directory in the module, mapped to the
// directories of this module's packages it imports. Test files are excluded:
// what a consumer links is what the non-test sources name.
func inModuleImports(t *testing.T, root, module string) map[string][]string {
	t.Helper()

	imports := map[string][]string{}

	must.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "artifacts" || d.Name() == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if parseErr != nil {
			return parseErr
		}

		rel, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return relErr
		}

		dir := filepath.ToSlash(rel)
		if _, ok := imports[dir]; !ok {
			imports[dir] = nil
		}

		for _, spec := range file.Imports {
			unquoted, quoteErr := strconv.Unquote(spec.Path.Value)
			if quoteErr != nil {
				return quoteErr
			}

			if !strings.HasPrefix(unquoted, module+"/") {
				continue
			}

			next := strings.TrimPrefix(unquoted, module+"/")
			if !slices.Contains(imports[dir], next) {
				imports[dir] = append(imports[dir], next)
			}
		}

		return nil
	}))

	must.MapNotEmpty(t, imports, must.Sprint("no packages walked, so this test asserted nothing"))

	return imports
}

// moduleRoot is one directory up, which is where this package sits and where
// go.mod has to be for the answer to be this module.
func moduleRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	must.NoError(t, err)
	must.FileExists(t, filepath.Join(root, "go.mod"))

	return root
}

// modulePath reads the module path out of go.mod rather than spelling it,
// because a major bump edits that file and would otherwise leave this test
// matching nothing and passing.
func modulePath(t *testing.T, root string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	must.NoError(t, err)

	for line := range strings.Lines(string(body)) {
		if after, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(after)
		}
	}

	t.Fatal("go.mod names no module path")

	return ""
}

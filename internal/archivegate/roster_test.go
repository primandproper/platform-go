package archivegate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is the mechanism the package documentation promises: the two
// halves of "no paged read can forget the archive grant", each read off the
// module's source rather than off a list somebody keeps.
//
// The first is that Filter is the only door. Every handler that turns a wire
// filter into a store filter does it through one of primitives-go's two
// converters, so either called anywhere but here is a read that converted its
// filter without naming a grant: the deprecated FromProto takes
// include_archived as sent, and QueryFilterFromProto takes whatever archive
// decision its caller hands it, ArchivedAllowed included.
//
// The second is that every door is used. A filtered RPC whose handler never
// reaches Filter is a read that either passes a filter it did not convert —
// which the first half already catches — or ignores the one the client sent,
// which this half catches: the walk goes from each RPC whose request carries a
// filtering.QueryFilter, read off the generated code, to the handler that
// serves it, and follows the calls inside that package until it finds Filter.

const (
	// thisPackage is where a converter may be called.
	thisPackage = "internal/archivegate"

	// gatePath is this package's import path, as a handler imports it.
	gatePath = "github.com/primandproper/platform-go/v15/internal/archivegate"

	// convertersPath is the converters' import path.
	convertersPath = "github.com/primandproper/primitives-go/v2/filtering/grpc"

	// queryFilterPath is the wire filter's package, which is how a request
	// carrying one is recognized.
	queryFilterPath = "github.com/primandproper/primitives-go/v2/filtering/filteringpb"
)

// converters are filtering/grpc's functions from a wire filter to a store one.
var converters = []string{"FromProto", "QueryFilterFromProto"}

// TestFilterIsTheOnlyDoor fails a module that converts a wire filter anywhere
// but Filter.
func TestFilterIsTheOnlyDoor(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)
	calls := 0

	for _, file := range sourceFiles(T, root) {
		name, ok := importName(file.ast, convertersPath)
		if !ok {
			continue
		}

		ast.Inspect(file.ast, func(n ast.Node) bool {
			converter := ""

			for _, candidate := range converters {
				if isCallTo(n, name, candidate) {
					converter = candidate
				}
			}

			if converter == "" {
				return true
			}

			calls++

			test.EqOp(T, thisPackage, file.dir, test.Sprintf(
				"%s converts a wire filter with %s, which decides include_archived without a grant; "+
					"call archivegate.Filter instead, naming the grant that archives what the read pages "+
					"(or archivegate.NothingArchived)", file.fset.Position(n.Pos()), converter))

			return true
		})
	}

	// A walk that found no call at all would pass a module whose import path
	// for the converter had moved, and would do it quietly.
	must.Positive(T, calls, must.Sprint("no converter is called anywhere, not even by Filter"))
}

// TestEveryFilteredRPCReachesFilter fails a module serving an RPC whose
// request carries a filter from a handler that never calls Filter.
func TestEveryFilteredRPCReachesFilter(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)
	files := sourceFiles(T, root)

	rpcs := filteredRPCs(T, files)
	servers := registrations(files)

	var checked []string

	for _, rpc := range rpcs {
		dir, ok := servers[rpc.service]
		if !ok {
			// A service nobody registers is served by nobody, and has no
			// handler to hold to the rule.
			continue
		}

		name := rpc.service + "." + rpc.method
		checked = append(checked, name)

		test.True(T, reaches(packageFiles(files, dir), rpc.method), test.Sprintf(
			"%s (served from %s) takes a filter and its handler never calls archivegate.Filter, so "+
				"include_archived reaches the store as the client sent it; read the filter through "+
				"archivegate.Filter, naming the grant that archives what the read pages", name, dir))
	}

	// The positive controls: the two surfaces this test exists because of, and
	// one of the surfaces it was modeled on. A walk that missed them would be
	// asserting nothing about anything.
	for _, want := range []string{
		"IdentityServiceServer.ListUsers",
		"OAuth2ClientsServiceServer.ListOAuth2Clients",
		"CommentsServiceServer.ListRootComments",
	} {
		test.SliceContains(T, checked, want, test.Sprintf("the walk never reached %s", want))
	}
}

// sourceFile is one parsed Go file and where it sits.
type sourceFile struct {
	ast  *ast.File
	fset *token.FileSet
	// dir is the file's directory relative to the module root, slash-separated.
	dir string
	// generated is whether the file is protoc's output.
	generated bool
}

// rpc is one server method whose request carries a wire filter.
type rpc struct {
	// service is the generated server interface, e.g. IdentityServiceServer.
	service string
	method  string
}

// filteredRPCs reads every generated server interface for the methods whose
// request message has a field of the wire filter's type.
func filteredRPCs(t *testing.T, files []*sourceFile) []rpc {
	t.Helper()

	// Request messages are keyed by directory as well as name, since two pb
	// packages may each declare a ListThingsRequest.
	filtered := map[string]bool{}

	for _, file := range files {
		if !file.generated {
			continue
		}

		filterPkg, ok := importName(file.ast, queryFilterPath)
		if !ok {
			continue
		}

		for _, spec := range typeSpecs(file.ast) {
			structType, isStruct := spec.Type.(*ast.StructType)
			if !isStruct {
				continue
			}

			for _, field := range structType.Fields.List {
				if isPointerTo(field.Type, filterPkg, "QueryFilter") {
					filtered[file.dir+"."+spec.Name.Name] = true
				}
			}
		}
	}

	var out []rpc

	for _, file := range files {
		if !file.generated || !strings.HasSuffix(file.fset.Position(file.ast.Pos()).Filename, "_grpc.pb.go") {
			continue
		}

		for _, spec := range typeSpecs(file.ast) {
			iface, isIface := spec.Type.(*ast.InterfaceType)
			name := spec.Name.Name

			if !isIface || !strings.HasSuffix(name, "Server") || strings.HasPrefix(name, "Unsafe") {
				continue
			}

			for _, method := range iface.Methods.List {
				fn, isFunc := method.Type.(*ast.FuncType)
				if !isFunc || len(method.Names) == 0 {
					continue
				}

				for _, param := range fn.Params.List {
					star, isStar := param.Type.(*ast.StarExpr)
					if !isStar {
						continue
					}

					if ident, isIdent := star.X.(*ast.Ident); isIdent && filtered[file.dir+"."+ident.Name] {
						out = append(out, rpc{service: name, method: method.Names[0].Name})
					}
				}
			}
		}
	}

	must.SliceNotEmpty(t, out, must.Sprint("no generated server interface takes a filtered request"))

	return out
}

// registrations maps each generated server interface to the directory of the
// hand-written package that registers an implementation of it.
func registrations(files []*sourceFile) map[string]string {
	out := map[string]string{}

	for _, file := range files {
		if file.generated {
			continue
		}

		ast.Inspect(file.ast, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasPrefix(sel.Sel.Name, "Register") || !strings.HasSuffix(sel.Sel.Name, "Server") {
				return true
			}

			out[strings.TrimPrefix(sel.Sel.Name, "Register")] = file.dir

			return true
		})
	}

	return out
}

// reaches reports whether the function or method named start, followed through
// every call to a function or method declared in the same package, calls
// archivegate.Filter.
//
// Calls are followed by name, not by type: a call to x.readFilter reaches every
// readFilter the package declares. That over-approximates, which is the safe
// direction for a check that a call happens — a package holding two methods of
// one name on two types is one this rule would not be the first to confuse.
func reaches(files []*sourceFile, start string) bool {
	decls := map[string][]*ast.FuncDecl{}
	gateName := map[*ast.FuncDecl]string{}

	for _, file := range files {
		name, _ := importName(file.ast, gatePath)

		for _, decl := range file.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			decls[fn.Name.Name] = append(decls[fn.Name.Name], fn)
			gateName[fn] = name
		}
	}

	seen := map[string]bool{start: true}
	queue := []string{start}

	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]

		for _, fn := range decls[next] {
			found := false

			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if found {
					return false
				}

				if gateName[fn] != "" && isCallTo(n, gateName[fn], "Filter") {
					found = true

					return false
				}

				if callee := calleeName(n); callee != "" && !seen[callee] {
					seen[callee] = true
					queue = append(queue, callee)
				}

				return true
			})

			if found {
				return true
			}
		}
	}

	return false
}

// calleeName is the name a call expression calls, or "" for anything else.
func calleeName(n ast.Node) string {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return ""
	}

	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	default:
		return ""
	}
}

// isCallTo reports whether n is a call to pkg.name.
func isCallTo(n ast.Node, pkg, name string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}

	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)

	return ok && ident.Name == pkg
}

// isPointerTo reports whether expr is *pkg.name.
func isPointerTo(expr ast.Expr, pkg, name string) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}

	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}

	ident, ok := sel.X.(*ast.Ident)

	return ok && ident.Name == pkg
}

// importName is the name a file refers to path by, if it imports it.
func importName(file *ast.File, path string) (string, bool) {
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil || imported != path {
			continue
		}

		if spec.Name != nil {
			return spec.Name.Name, true
		}

		return imported[strings.LastIndex(imported, "/")+1:], true
	}

	return "", false
}

// typeSpecs are a file's top-level type declarations.
func typeSpecs(file *ast.File) []*ast.TypeSpec {
	var out []*ast.TypeSpec

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}

		for _, spec := range gen.Specs {
			if typeSpec, isType := spec.(*ast.TypeSpec); isType {
				out = append(out, typeSpec)
			}
		}
	}

	return out
}

// packageFiles are the hand-written files in dir.
func packageFiles(files []*sourceFile, dir string) []*sourceFile {
	return slices.DeleteFunc(slices.Clone(files), func(f *sourceFile) bool {
		return f.dir != dir || f.generated
	})
}

// sourceFiles parses every non-test Go file in the module.
func sourceFiles(t *testing.T, root string) []*sourceFile {
	t.Helper()

	var out []*sourceFile

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// A dot directory can hold another checkout of this module, and
			// artifacts/ is where proto.sh unpacks a pinned protoc.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts" || name == "testdata") {
				return filepath.SkipDir
			}

			return nil
		}

		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()

		parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}

		out = append(out, &sourceFile{
			ast:       parsed,
			fset:      fset,
			dir:       filepath.ToSlash(rel),
			generated: strings.HasSuffix(path, ".pb.go"),
		})

		return nil
	})
	must.NoError(t, err)
	must.SliceNotEmpty(t, out)

	return out
}

// moduleRoot is the directory holding go.mod, two above this package.
func moduleRoot(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(t, err)
	must.FileExists(t, filepath.Join(root, "go.mod"))

	return root
}

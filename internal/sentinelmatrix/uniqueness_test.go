package sentinelmatrix_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test/must"
)

// TestSentinelMessagesAreUniqueAcrossTheModule pins a property nothing else in
// the module can see, because it is only violated between two packages that
// never import one another.
//
// A sentinel that crosses a gRPC connection does not arrive as itself. The
// server encodes it with cockroachdb's EncodeError and errors/grpc's decoding
// interceptor rebuilds it on the client, and the rebuilt value cannot be the
// variable the server declared — it is a fresh allocation in another process.
// What survives the wire is the sentinel's *mark*: the chain of type names and
// the message cockroachdb records for it. The interceptor therefore matches a
// decoded error against a sentinel by mark, not by identity, which is what
// makes std errors.Is work across a connection at all.
//
// Two sentinels declared with the same wording have the same mark, because both
// are a plain cockroachdb leaf with that message, and after a round trip they
// are indistinguishable. identity.ErrScopeMismatch and
// notifications.ErrScopeMismatch were the case that found this: both were
// declared as "entity names a different scope than the write", and a client
// holding whichever one the server sent got true from errors.Is against both.
// A handler branching on the notifications sentinel then took the identity
// branch, or the other way round, depending on nothing it could observe. The
// same collapse turns a package's "nil HTTP client" into every other package's
// "nil HTTP client" — less dangerous only because nobody branches on it, and
// still a message that names the wrong component in a client's log.
//
// So identical wording across packages is a wire bug rather than a style nit,
// and the fix is what this test enforces: every sentinel's message names its
// own noun and appears once in the module. Only string-literal messages are
// read, because a message built at runtime is not a sentinel in the sense a
// client can match. For Wrap and Wrapf the key is the wrapped expression as
// written together with the message, since the wire mark of Wrap(x, msg) is
// msg prefixed onto x's mark and two wraps of the same platform sentinel with
// the same words collide exactly as two News do.
//
// Every non-test, non-generated file in the module is walked rather than the
// five packages in Packages, because the sentinels that collided were not in
// those five, and nothing about a package's tier decides whether its errors
// travel over gRPC.
func TestSentinelMessagesAreUniqueAcrossTheModule(t *testing.T) {
	t.Parallel()

	root := moduleRootPath()
	platformErrorsPath := primitivesPath(t, root) + "/errors"

	declared := map[string][]sentinelDeclaration{}
	files := 0

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if entry.IsDir() {
			if skipDirectory(entry.Name()) {
				return filepath.SkipDir
			}

			return nil
		}

		if !isSourceFile(path) {
			return nil
		}

		fset := token.NewFileSet()

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return parseErr
		}

		if ast.IsGenerated(file) {
			return nil
		}

		files++

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}

		for _, decl := range sentinelDeclarationsIn(fset, file, rel, platformErrorsPath) {
			declared[decl.key] = append(declared[decl.key], decl)
		}

		return nil
	})
	must.NoError(t, err)
	must.Positive(t, files, must.Sprint("no source files walked, so this test asserted nothing"))
	must.Positive(t, len(declared), must.Sprint("no sentinels parsed out of the module, so this test asserted nothing"))

	var duplicated []string

	for key, decls := range declared {
		if len(decls) < 2 {
			continue
		}

		slices.SortFunc(decls, func(a, b sentinelDeclaration) int {
			return strings.Compare(a.position, b.position)
		})

		lines := make([]string, 0, len(decls))
		for _, decl := range decls {
			lines = append(lines, fmt.Sprintf("\t%s\t%s", decl.position, decl.name))
		}

		duplicated = append(duplicated, fmt.Sprintf("%q is declared %d times:\n%s", key, len(decls), strings.Join(lines, "\n")))
	}

	slices.Sort(duplicated)

	if len(duplicated) > 0 {
		t.Fatalf(
			"%d sentinel messages are declared more than once. Two sentinels with the same wording have the same "+
				"cockroachdb mark, and errors/grpc matches a decoded error by mark, so a client cannot tell them apart "+
				"after a round trip. Reword each so it names its own package's noun.\n\n%s",
			len(duplicated), strings.Join(duplicated, "\n\n"))
	}
}

// sentinelDeclaration is one package-level Err var whose value is a call to one
// of the error constructors with a literal message.
type sentinelDeclaration struct {
	// key is the message for New and Newf, and "<wrapped expression>: <message>"
	// for Wrap and Wrapf.
	key string
	// name is the variable's name.
	name string
	// position is file:line relative to the module root.
	position string
}

// constructors are the names on the errors packages whose result is a sentinel
// with a message a client can match on.
var constructors = map[string]bool{
	"New":   true,
	"Newf":  true,
	"Wrap":  true,
	"Wrapf": true,
}

// wrapping says which constructors take the wrapped error first and the message
// second.
var wrapping = map[string]bool{
	"Wrap":  true,
	"Wrapf": true,
}

// sentinelDeclarationsIn reads one file's package-level Err vars that are built
// by calling New, Newf, Wrap or Wrapf on the platform errors package or on
// cockroachdb's, through whatever name the file imported them under.
func sentinelDeclarationsIn(fset *token.FileSet, file *ast.File, rel, platformErrorsPath string) []sentinelDeclaration {
	aliases := errorsAliases(file, platformErrorsPath)

	var found []sentinelDeclaration

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if !isValue || len(value.Names) != len(value.Values) {
				continue
			}

			for i, ident := range value.Names {
				if !strings.HasPrefix(ident.Name, "Err") {
					continue
				}

				call, isCall := value.Values[i].(*ast.CallExpr)
				if !isCall {
					continue
				}

				constructor, isConstructor := constructorName(call.Fun, aliases)
				if !isConstructor {
					continue
				}

				key, hasKey := sentinelKey(constructor, call.Args)
				if !hasKey {
					continue
				}

				pos := fset.Position(ident.Pos())

				found = append(found, sentinelDeclaration{
					key:      key,
					name:     ident.Name,
					position: fmt.Sprintf("%s:%d", rel, pos.Line),
				})
			}
		}
	}

	return found
}

// errorsAliases is the set of local names under which file imports the platform
// errors package or cockroachdb's.
func errorsAliases(file *ast.File, platformErrorsPath string) map[string]bool {
	aliases := map[string]bool{}

	for _, imported := range file.Imports {
		path := strings.Trim(imported.Path.Value, `"`)
		if path != platformErrorsPath && path != "github.com/cockroachdb/errors" {
			continue
		}

		name := "errors"
		if imported.Name != nil {
			name = imported.Name.Name
		}

		aliases[name] = true
	}

	return aliases
}

// constructorName reports which constructor a call's function names, if it is
// one: a selector on an errors alias. There is no bare form to admit — the
// errors package is in primitives-go, so every declaration this walk can reach
// names its constructor through an import.
func constructorName(fun ast.Expr, aliases map[string]bool) (string, bool) {
	selector, isSelector := fun.(*ast.SelectorExpr)
	if !isSelector {
		return "", false
	}

	pkg, isIdent := selector.X.(*ast.Ident)
	if !isIdent || !aliases[pkg.Name] || !constructors[selector.Sel.Name] {
		return "", false
	}

	return selector.Sel.Name, true
}

// sentinelKey derives the key a declaration collides on. It is the literal
// message for New and Newf, and the wrapped expression's text joined to the
// literal message for Wrap and Wrapf. A message that is not a string literal
// yields no key.
func sentinelKey(constructor string, args []ast.Expr) (string, bool) {
	messageIndex := 0
	if wrapping[constructor] {
		messageIndex = 1
	}

	if len(args) <= messageIndex {
		return "", false
	}

	lit, isLit := args[messageIndex].(*ast.BasicLit)
	if !isLit || lit.Kind != token.STRING {
		return "", false
	}

	message, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}

	if !wrapping[constructor] {
		return message, true
	}

	return types.ExprString(args[0]) + ": " + message, true
}

// skipDirectory names the directories the walk does not enter: version control,
// editor and agent state, a stale local vendor tree, and test fixtures.
func skipDirectory(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata"
}

// isSourceFile is true for a non-test, non-protobuf Go file.
func isSourceFile(path string) bool {
	return strings.HasSuffix(path, ".go") &&
		!strings.HasSuffix(path, "_test.go") &&
		!strings.HasSuffix(path, ".pb.go")
}

// primitivesPath reads the primitives-go requirement out of root's go.mod, so
// that the errors import path is derived rather than spelled here with a major
// version that a bump would have to remember to change. It is the same reason
// the path used to be read off the module line: primitives-go is on v1 and
// carries no suffix today, and the day it carries one this reads it.
func primitivesPath(t *testing.T, root string) string {
	t.Helper()

	f, err := os.Open(filepath.Join(root, "go.mod"))
	must.NoError(t, err)

	defer func() { must.NoError(t, f.Close()) }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.HasPrefix(fields[0], primitivesModule) {
			return fields[0]
		}
	}

	must.NoError(t, scanner.Err())
	t.Fatalf("go.mod requires no module under %s", primitivesModule)

	return ""
}

// primitivesModule is the primitives module without a major-version suffix,
// which is the prefix every major of it shares.
const primitivesModule = "github.com/primandproper/primitives-go"

package protoconvention_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestGoTagsSpellTheSameRule is the half of this package's claim that the
// descriptor sweep does not reach.
//
// The rule is an agreement between two descriptions of one field, and the sweep
// beside this one checks only the .proto side: it asserts that every descriptor
// spells its JSON name the way jsonName says, which is a statement about
// protobuf and nothing at all about Go. A module whose descriptors all said
// "resourceID" while its structs said `json:"resourceId"` would pass it
// completely, and the two would still disagree on the wire — which is the exact
// failure this package was written to end.
//
// Pairing every message with the Go type it renders beside would close it the
// way identity/grpc and waitlists/grpc do, and would need a roster naming both
// halves of every pair in eleven schemas — most of whose messages are requests
// and responses with no Go counterpart to pair with. This closes it from the
// other direction and more widely: both descriptions are held to one rule
// independently, so they agree by construction, and a Go tag anywhere in the
// module is covered rather than only one inside a message somebody rostered.
//
// The rule here is the same sentence as jsonName's, read backwards. protoc's
// derivation and Go's differ only at a trailing id, so that is the only place a
// tag can disagree: this module spells it ID and IDs, and a tag ending in Id or
// Ids is the spelling protobuf would have derived on its own.
func TestGoTagsSpellTheSameRule(T *testing.T) {
	T.Parallel()

	root := moduleRoot(T)
	checked := 0

	for _, path := range goFiles(T, root) {
		fset := token.NewFileSet()

		parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		must.NoError(T, err, must.Sprintf("parsing %s", path))

		ast.Inspect(parsed, func(n ast.Node) bool {
			structType, ok := n.(*ast.StructType)
			if !ok || structType.Fields == nil {
				return true
			}

			for _, field := range structType.Fields.List {
				name, tagged := jsonTag(T, field)
				if !tagged {
					continue
				}

				checked++

				test.False(T, endsInDerivedID(name), test.Sprintf(
					"%s spells a JSON name %q, and this module spells a trailing id ID — "+
						"which is the one place protobuf's derivation and Go's disagree, and so the one "+
						"place a struct tag and a descriptor can describe one field two ways",
					fset.Position(field.Pos()), name))
			}

			return true
		})
	}

	// A walk that found nothing would assert nothing, and would do it quietly.
	must.Positive(T, checked, must.Sprint("no json tags were found anywhere in the module"))
}

// jsonTag reads a field's JSON name, reporting whether it has one worth
// checking. A field tagged "-" is not on the wire under any name.
func jsonTag(t *testing.T, field *ast.Field) (string, bool) {
	t.Helper()

	if field.Tag == nil {
		return "", false
	}

	unquoted, err := strconv.Unquote(field.Tag.Value)
	must.NoError(t, err, must.Sprintf("unquoting the struct tag %s", field.Tag.Value))

	name, _, _ := strings.Cut(reflect.StructTag(unquoted).Get("json"), ",")
	if name == "" || name == "-" {
		return "", false
	}

	return name, true
}

// endsInDerivedID reports whether a JSON name ends in the spelling protobuf
// would have derived rather than the one this module uses.
//
// It is deliberately capital-sensitive and deliberately narrow. "uuid" and
// "euclid" end in the letters i and d and are not identifiers spelled wrong;
// what the rule is about is a camel-cased word boundary, which is the capital
// I. A name that is exactly "id" is right on both sides, since protoc leaves a
// single-segment name's first character alone.
func endsInDerivedID(name string) bool {
	return strings.HasSuffix(name, "Id") || strings.HasSuffix(name, "Ids")
}

// goFiles is every .go file in the module, by absolute path.
//
// The generated bindings are included rather than skipped, and need no
// exception: protoc-gen-go tags a field with the proto field name, so a .pb.go
// carries `json:"owner_user_id,omitempty"` — snake_case, which cannot end in a
// camel-cased Id and so is out of this rule's reach by construction rather than
// by a skip list somebody has to maintain.
func goFiles(t *testing.T, root string) []string {
	t.Helper()

	found := []string{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		if d.IsDir() {
			// The same two the schema walk skips, for the same reasons: a dot
			// directory can hold another checkout of this module, and artifacts/
			// is where proto.sh unpacks a pinned protoc.
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "artifacts") {
				return filepath.SkipDir
			}

			return nil
		}

		if filepath.Ext(path) == ".go" {
			found = append(found, path)
		}

		return nil
	})
	must.NoError(t, err)
	must.SliceNotEmpty(t, found)

	return found
}

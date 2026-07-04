package ast

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// structTypeFromSource parses a single struct declaration from source and returns its
// *ast.StructType, so tests can exercise realistic field types (pointers, slices, maps,
// generics, embedded fields) without hand-building the AST.
func structTypeFromSource(t *testing.T, src string) *ast.StructType {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), "src.go", "package p\n"+src, 0)
	must.NoError(t, err)

	gd, ok := f.Decls[0].(*ast.GenDecl)
	must.True(t, ok)
	ts, ok := gd.Specs[0].(*ast.TypeSpec)
	must.True(t, ok)
	st, ok := ts.Type.(*ast.StructType)
	must.True(t, ok)

	return st
}

func TestGetModulePath(T *testing.T) {
	T.Parallel()

	T.Run("reads module path from go.mod", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/example/test\n\ngo 1.21\n"), 0o600)
		must.NoError(t, err)

		path, err := GetModulePath(dir)

		must.NoError(t, err)
		test.EqOp(t, "github.com/example/test", path)
	})

	T.Run("returns error when go.mod does not exist", func(t *testing.T) {
		t.Parallel()

		path, err := GetModulePath(t.TempDir())

		test.EqOp(t, "", path)
		test.Error(t, err)
	})

	T.Run("returns error when no module directive found", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("go 1.21\n"), 0o600)
		must.NoError(t, err)

		path, err := GetModulePath(dir)

		test.EqOp(t, "", path)
		test.Error(t, err)
	})
}

func TestBuildImportMap(T *testing.T) {
	T.Parallel()

	T.Run("builds map from imports", func(t *testing.T) {
		t.Parallel()

		file := &ast.File{
			Imports: []*ast.ImportSpec{
				{Path: &ast.BasicLit{Value: `"fmt"`}},
				{Path: &ast.BasicLit{Value: `"github.com/example/pkg"`}},
			},
		}

		result := BuildImportMap(file)

		test.EqOp(t, "fmt", result["fmt"])
		test.EqOp(t, "github.com/example/pkg", result["pkg"])
	})

	T.Run("handles aliased imports", func(t *testing.T) {
		t.Parallel()

		file := &ast.File{
			Imports: []*ast.ImportSpec{
				{
					Name: &ast.Ident{Name: "myfmt"},
					Path: &ast.BasicLit{Value: `"fmt"`},
				},
			},
		}

		result := BuildImportMap(file)

		test.EqOp(t, "fmt", result["myfmt"])
	})

	T.Run("excludes blank and dot imports", func(t *testing.T) {
		t.Parallel()

		file := &ast.File{
			Imports: []*ast.ImportSpec{
				{
					Name: &ast.Ident{Name: "_"},
					Path: &ast.BasicLit{Value: `"image/png"`},
				},
				{
					Name: &ast.Ident{Name: "."},
					Path: &ast.BasicLit{Value: `"testing"`},
				},
			},
		}

		result := BuildImportMap(file)

		test.MapEmpty(t, result)
	})

	T.Run("skips imports with nil path", func(t *testing.T) {
		t.Parallel()

		file := &ast.File{
			Imports: []*ast.ImportSpec{
				{Path: nil},
			},
		}

		result := BuildImportMap(file)

		test.MapEmpty(t, result)
	})
}

func TestFilterModuleImports(T *testing.T) {
	T.Parallel()

	T.Run("filters to module-internal imports", func(t *testing.T) {
		t.Parallel()

		imports := map[string]string{
			"fmt":     "fmt",
			"logging": "github.com/example/mod/observability/logging",
			"errors":  "github.com/example/mod/errors",
		}

		result := FilterModuleImports(imports, "github.com/example/mod")

		test.MapLen(t, 2, result)
		test.EqOp(t, "observability/logging", result["logging"])
		test.EqOp(t, "errors", result["errors"])
	})

	T.Run("returns empty map when no module imports", func(t *testing.T) {
		t.Parallel()

		imports := map[string]string{
			"fmt": "fmt",
		}

		result := FilterModuleImports(imports, "github.com/example/mod")

		test.MapEmpty(t, result)
	})
}

func TestGetTagValue(T *testing.T) {
	T.Parallel()

	T.Run("extracts tag value", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "name", GetTagValue(`json:"name"`, "json"))
	})

	T.Run("extracts tag value with omitempty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "name", GetTagValue(`json:"name,omitempty"`, "json"))
	})

	T.Run("extracts from multiple tags", func(t *testing.T) {
		t.Parallel()

		tag := `json:"name" env:"MY_VAR"`
		test.EqOp(t, "name", GetTagValue(tag, "json"))
		test.EqOp(t, "MY_VAR", GetTagValue(tag, "env"))
	})

	T.Run("returns empty for missing key", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", GetTagValue(`json:"name"`, "xml"))
	})

	T.Run("handles backtick-wrapped tags", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "name", GetTagValue("`json:\"name\"`", "json"))
	})
}

func TestGetStructFields(T *testing.T) {
	T.Parallel()

	T.Run("returns field names and types", func(t *testing.T) {
		t.Parallel()

		st := &ast.StructType{
			Fields: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{{Name: "Name"}},
						Type:  &ast.Ident{Name: "string"},
					},
					{
						Names: []*ast.Ident{{Name: "Logger"}},
						Type: &ast.SelectorExpr{
							X:   &ast.Ident{Name: "logging"},
							Sel: &ast.Ident{Name: "Logger"},
						},
					},
				},
			},
		}

		fields := GetStructFields(st)

		test.EqOp(t, "string", fields["Name"])
		test.EqOp(t, "logging.Logger", fields["Logger"])
	})

	T.Run("excludes underscore fields", func(t *testing.T) {
		t.Parallel()

		st := &ast.StructType{
			Fields: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{{Name: "_"}},
						Type:  &ast.Ident{Name: "int"},
					},
				},
			},
		}

		fields := GetStructFields(st)

		test.MapEmpty(t, fields)
	})

	T.Run("handles multiple names per field", func(t *testing.T) {
		t.Parallel()

		st := &ast.StructType{
			Fields: &ast.FieldList{
				List: []*ast.Field{
					{
						Names: []*ast.Ident{
							{Name: "X", NamePos: token.NoPos},
							{Name: "Y", NamePos: token.NoPos},
						},
						Type: &ast.Ident{Name: "int"},
					},
				},
			},
		}

		fields := GetStructFields(st)

		test.EqOp(t, "int", fields["X"])
		test.EqOp(t, "int", fields["Y"])
	})

	T.Run("renders pointer, slice, map, and generic field types", func(t *testing.T) {
		t.Parallel()

		st := structTypeFromSource(t, `type S struct {
			Ptr     *Foo
			Bytes   []byte
			Lookup  map[string]int
			Generic Box[string]
			Qual    time.Time
		}`)

		fields := GetStructFields(st)

		test.EqOp(t, "*Foo", fields["Ptr"])
		test.EqOp(t, "[]byte", fields["Bytes"])
		test.EqOp(t, "map[string]int", fields["Lookup"])
		test.EqOp(t, "Box[string]", fields["Generic"])
		test.EqOp(t, "time.Time", fields["Qual"])
	})

	T.Run("keys embedded fields by their base type name", func(t *testing.T) {
		t.Parallel()

		st := structTypeFromSource(t, `type S struct {
			BaseError
			*Embedded
			pkg.Qualified
			Field string
		}`)

		fields := GetStructFields(st)

		test.EqOp(t, "BaseError", fields["BaseError"])
		test.EqOp(t, "*Embedded", fields["Embedded"])
		test.EqOp(t, "pkg.Qualified", fields["Qualified"])
		test.EqOp(t, "string", fields["Field"])
	})
}

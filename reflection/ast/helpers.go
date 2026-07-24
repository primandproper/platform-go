package ast

import (
	"bufio"
	goast "go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"github.com/primandproper/platform-go/v6/errors"
)

// GetModulePath reads the module path from the go.mod file in the given directory.
func GetModulePath(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", errors.Wrap(err, "opening go.mod")
	}
	defer func() {
		_ = f.Close() //nolint:errcheck // read-only file; close error is not actionable here
	}()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}

	if err = scanner.Err(); err != nil {
		return "", errors.Wrap(err, "scanning go.mod")
	}

	return "", errors.New("no module directive found in go.mod")
}

// BuildImportMap returns a map from each import's local name (explicit alias or
// inferred last path segment) to its full import path. Blank ("_") and dot (".")
// imports are excluded.
func BuildImportMap(file *goast.File) map[string]string {
	result := make(map[string]string)

	for _, imp := range file.Imports {
		if imp.Path == nil {
			continue
		}

		importPath := strings.Trim(imp.Path.Value, `"`)

		var localName string
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				continue
			}
			localName = imp.Name.Name
		} else {
			parts := strings.Split(importPath, "/")
			localName = parts[len(parts)-1]
		}

		result[localName] = importPath
	}

	return result
}

// FilterModuleImports filters an import map to only include module-internal imports
// and converts the values from full import paths to module-relative directory paths.
func FilterModuleImports(imports map[string]string, modulePath string) map[string]string {
	result := make(map[string]string)
	prefix := modulePath + "/"

	for localName, importPath := range imports {
		if after, ok := strings.CutPrefix(importPath, prefix); ok {
			result[localName] = after
		}
	}

	return result
}

// GetTagValue extracts the value of a specific tag key from a raw struct field
// tag string (with or without surrounding backticks). It returns the value before
// any comma (i.e., omitting options like "omitempty"), with surrounding quotes stripped.
// Returns empty string if the key is not found.
func GetTagValue(tag, key string) string {
	tag = strings.Trim(tag, "`")

	for t := range strings.SplitSeq(tag, " ") {
		parts := strings.SplitN(t, ":", 2)
		if len(parts) == 2 && parts[0] == key {
			return strings.Trim(strings.Split(parts[1], ",")[0], `"`)
		}
	}

	return ""
}

// GetStructFields returns a map of field names to their type representation
// from an *ast.StructType. Fields named "_" are excluded.
//
// The type representation is the field type rendered as Go source, so all field
// kinds are handled: "TypeName" (local), "pkg.TypeName" (imported), "*T" (pointer),
// "[]byte" (slice/array), "map[string]int" (map), "Foo[T]" (generic), and so on.
// Embedded (anonymous) fields are keyed by the embedded type's base name — e.g. an
// embedded "pkg.Base" or "*pkg.Base" is keyed "Base". An embedded field whose name
// cannot be derived (rare, e.g. an anonymous instantiated type with no resolvable
// base ident) is skipped.
func GetStructFields(structType *goast.StructType) map[string]string {
	fields := make(map[string]string)

	for _, field := range structType.Fields.List {
		fieldType := types.ExprString(field.Type)

		if len(field.Names) == 0 {
			// Embedded/anonymous field: derive the name from the type itself.
			if name := embeddedFieldName(field.Type); name != "" {
				fields[name] = fieldType
			}
			continue
		}

		for _, name := range field.Names {
			if name.Name != "_" {
				fields[name.Name] = fieldType
			}
		}
	}

	return fields
}

// embeddedFieldName derives the field name Go assigns to an embedded field from its
// type expression: the base type identifier, ignoring any leading pointer and any
// generic type arguments (e.g. "*pkg.Base[T]" is embedded as field "Base").
func embeddedFieldName(expr goast.Expr) string {
	switch t := expr.(type) {
	case *goast.StarExpr:
		return embeddedFieldName(t.X)
	case *goast.Ident:
		return t.Name
	case *goast.SelectorExpr:
		return t.Sel.Name
	case *goast.IndexExpr:
		return embeddedFieldName(t.X)
	case *goast.IndexListExpr:
		return embeddedFieldName(t.X)
	default:
		return ""
	}
}

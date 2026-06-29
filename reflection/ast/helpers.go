package ast

import (
	"bufio"
	"fmt"
	goast "go/ast"
	"os"
	"path/filepath"
	"strings"

	"github.com/primandproper/platform-go/v2/errors"
)

// GetModulePath reads the module path from the go.mod file in the given directory.
func GetModulePath(dir string) (string, error) {
	f, err := os.Open(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", errors.Wrap(err, "opening go.mod")
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}

	if err = f.Close(); err != nil {
		return "", errors.Wrap(err, "closing go.mod file")
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
// Type representations: "TypeName" for local types, "pkg.TypeName" for imported types.
func GetStructFields(structType *goast.StructType) map[string]string {
	fields := make(map[string]string)

	for _, field := range structType.Fields.List {
		fieldType := ""
		switch t := field.Type.(type) {
		case *goast.Ident:
			fieldType = t.Name
		case *goast.SelectorExpr:
			fieldType = fmt.Sprintf("%s.%s", t.X, t.Sel.Name)
		}

		for _, name := range field.Names {
			if name.Name != "_" {
				fields[name.Name] = fieldType
			}
		}
	}

	return fields
}

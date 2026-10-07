package mcptool

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/format"
	"go/parser"
	"go/token"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/google/jsonschema-go/jsonschema"
)

// Docs is each documented struct's field descriptions, keyed by the struct's
// import path and name ("example.com/pkg.Report"), then by the field's JSON
// property name.
//
// A surface carries one as a generated variable, because the doc comments it
// holds are source and a running binary has none. See [Extract].
type Docs map[string]map[string]string

// Output reflects T into the schema a tool's structured output is described
// and validated with.
//
// Property names and types are reflected off T, and each property's
// description is its field's doc comment from docs. A tenancy.Scope is a
// string or null, which is what it marshals to; types maps any other type whose JSON
// is not what its Go shape suggests, or whose values are a closed set worth
// naming as an enum. A *filtering.QueryFilter anywhere in T is described by
// filtering.QueryFilterSchema rather than reflected again.
//
// It panics on a type that does not reflect, which is a property of a type
// this module owns and is caught by the surface's own tests before it ships.
func Output[T any](docs Docs, types map[reflect.Type]*jsonschema.Schema) *jsonschema.Schema {
	return reflectSchema(reflect.TypeFor[T](), docs, types)
}

// Input reflects T into a tool's input schema. It is [Output] for the type a
// tool decodes its arguments into, so the property a model is told to send is
// the property the handler reads — the field's `json` tag — and never a second
// spelling of it.
func Input[T any](docs Docs, types map[reflect.Type]*jsonschema.Schema) *jsonschema.Schema {
	return reflectSchema(reflect.TypeFor[T](), docs, types)
}

var (
	scopeType       = reflect.TypeFor[tenancy.Scope]()
	queryFilterType = reflect.TypeFor[filtering.QueryFilter]()
)

func reflectSchema(t reflect.Type, docs Docs, types map[reflect.Type]*jsonschema.Schema) *jsonschema.Schema {
	// A scope marshals to its owner identifier, "" for Global, and an unset
	// one to null — webhooks.Endpoint.CreatedBy is unset wherever nobody
	// recorded who registered the endpoint.
	overrides := map[reflect.Type]*jsonschema.Schema{scopeType: {Types: []string{"null", "string"}}}
	maps.Copy(overrides, types)

	schema, err := jsonschema.ForType(t, &jsonschema.ForOptions{TypeSchemas: overrides})
	if err != nil {
		panic(fmt.Sprintf("mcptool: reflecting %v: %v", t, err))
	}

	merged := Docs{}
	for _, source := range []Docs{undocumented, fieldDocs, docs} {
		for spec, fields := range source {
			if merged[spec] == nil {
				merged[spec] = map[string]string{}
			}

			maps.Copy(merged[spec], fields)
		}
	}

	describe(schema, t, merged)

	return schema
}

// undocumented describes the fields of primitives-go's paged result that carry
// no doc comment of their own to extract — totalCount shares filteredCount's,
// and data and maxResponseSize have none. It is the one hand-written entry
// here, and it is kept to fields whose meaning is their type's: a field
// documented upstream later is extracted into fieldDocs and wins over this.
var undocumented = Docs{
	"github.com/primandproper/primitives-go/v2/filtering.QueryFilteredResult": {
		"data": "Data is the rows on this page, in the order the filter asked for.",
	},
	"github.com/primandproper/primitives-go/v2/filtering.Pagination": {
		"totalCount":      "TotalCount is how many rows were in scope regardless of the filter. It means nothing unless countsKnown is set.",
		"maxResponseSize": "MaxResponseSize is the page size this page was answered with, after the default and the ceiling were applied.",
	},
}

// describe walks t beside the schema reflected from it, writing each field's
// doc comment onto its property and substituting the filter's own schema.
func describe(schema *jsonschema.Schema, t reflect.Type, docs Docs) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if schema == nil {
		return
	}

	switch t.Kind() {
	case reflect.Slice, reflect.Array:
		describe(schema.Items, t.Elem(), docs)

		return
	case reflect.Struct:
	default:
		return
	}

	if schema.Properties == nil {
		// A struct reflected as a scalar: time.Time, or an override.
		return
	}

	described := docs[typeKey(t)]

	for field := range t.Fields() {
		if !field.IsExported() {
			continue
		}

		name, embedded := jsonName(&field)
		if name == "-" {
			continue
		}

		if embedded {
			describe(schema, field.Type, docs)

			continue
		}

		property, ok := schema.Properties[name]
		if !ok {
			continue
		}

		if elem := derefType(field.Type); elem == queryFilterType {
			property = queryFilterSchema(field.Type.Kind() == reflect.Pointer)
			schema.Properties[name] = property
		} else {
			describe(property, field.Type, docs)
		}

		if description := described[name]; description != "" {
			property.Description = description
		}
	}
}

// queryFilterSchema is filtering.QueryFilterSchema as a *jsonschema.Schema,
// admitting null where the field holding it is a pointer.
func queryFilterSchema(nullable bool) *jsonschema.Schema {
	raw, err := json.Marshal(filtering.QueryFilterSchema())
	if err != nil {
		panic(fmt.Sprintf("mcptool: marshaling the query filter schema: %v", err))
	}

	var schema jsonschema.Schema
	if err = json.Unmarshal(raw, &schema); err != nil {
		panic(fmt.Sprintf("mcptool: decoding the query filter schema: %v", err))
	}

	if nullable {
		schema.Types = []string{"null", schema.Type}
		schema.Type = ""
	}

	return &schema
}

// Undescribed lists every property in schema, nested ones included, that
// carries no description — the paths a surface's test asserts are none.
func Undescribed(schema *jsonschema.Schema) []string {
	var missing []string

	var walk func(prefix string, s *jsonschema.Schema)

	walk = func(prefix string, s *jsonschema.Schema) {
		if s == nil {
			return
		}

		walk(prefix+"[]", s.Items)

		for name, property := range s.Properties {
			path := strings.TrimPrefix(prefix+"."+name, ".")
			if property.Description == "" {
				missing = append(missing, path)
			}

			walk(path, property)
		}
	}

	walk("", schema)
	slices.Sort(missing)

	return missing
}

// Extract reads the field doc comments of each named struct out of its
// package's source. A spec is an import path and a type name joined by a dot:
// "github.com/primandproper/platform-go/v15/issuereports.Report".
//
// A field's description is the first paragraph of its doc comment, or of its
// line comment where it has no doc comment, with the line breaks folded. The
// first paragraph is the sentence that says what the field is; what follows it
// in this module is the reasoning behind it, which a model asking what a field
// holds does not need and pays for on every tool listing.
func Extract(specs ...string) (Docs, error) {
	docs := Docs{}

	for _, spec := range specs {
		dot := strings.LastIndex(spec, ".")
		if dot < 0 {
			return nil, fmt.Errorf("mcptool: %q names no type; the form is <import path>.<Type>", spec)
		}

		importPath, name := spec[:dot], spec[dot+1:]

		fields, err := extractType(importPath, name)
		if err != nil {
			return nil, err
		}

		docs[spec] = fields
	}

	return docs, nil
}

func extractType(importPath, typeName string) (map[string]string, error) {
	pkg, err := build.Import(importPath, ".", build.FindOnly)
	if err != nil {
		return nil, fmt.Errorf("mcptool: locating %s: %w", importPath, err)
	}

	files, err := filepath.Glob(filepath.Join(pkg.Dir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("mcptool: listing %s: %w", pkg.Dir, err)
	}

	fset := token.NewFileSet()

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return nil, fmt.Errorf("mcptool: parsing %s: %w", path, parseErr)
		}

		if fields, ok := findStruct(file, typeName); ok {
			return fields, nil
		}
	}

	return nil, fmt.Errorf("mcptool: %s declares no struct %s", importPath, typeName)
}

func findStruct(file *ast.File, typeName string) (map[string]string, bool) {
	var (
		fields map[string]string
		found  bool
	)

	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || found || spec.Name.Name != typeName {
			return !found
		}

		st, ok := spec.Type.(*ast.StructType)
		if !ok {
			return false
		}

		found = true
		fields = map[string]string{}

		for _, field := range st.Fields.List {
			description := firstParagraph(field.Doc)
			if description == "" {
				description = firstParagraph(field.Comment)
			}

			for _, ident := range field.Names {
				if !ident.IsExported() {
					continue
				}

				name := ident.Name
				if field.Tag != nil {
					if tag, err := strconv.Unquote(field.Tag.Value); err == nil {
						if jsonTag, _, _ := strings.Cut(reflect.StructTag(tag).Get("json"), ","); jsonTag != "" {
							name = jsonTag
						}
					}
				}

				if name == "-" || description == "" {
					continue
				}

				fields[name] = description
			}
		}

		return false
	})

	return fields, found
}

func firstParagraph(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}

	paragraph, _, _ := strings.Cut(group.Text(), "\n\n")

	return strings.Join(strings.Fields(paragraph), " ")
}

// Render writes docs as the Go source of a generated file in package pkg,
// declaring them as the variable name.
func Render(pkg, name string, docs Docs) ([]byte, error) {
	var b strings.Builder

	b.WriteString("// Code generated by mcptool/mcpdocs; DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "package %s\n\n", pkg)
	qualifier := "mcptool."
	if pkg == "mcptool" {
		qualifier = ""
	} else {
		b.WriteString("import \"github.com/primandproper/platform-go/v15/mcptool\"\n\n")
	}

	fmt.Fprintf(&b, "// %s is the field doc comments the tool schemas here describe their\n", name)
	b.WriteString("// properties with, read out of the source by Extract.\n")
	fmt.Fprintf(&b, "var %s = %sDocs{\n", name, qualifier)

	for _, spec := range slices.Sorted(maps.Keys(docs)) {
		fmt.Fprintf(&b, "%q: {\n", spec)

		for _, field := range slices.Sorted(maps.Keys(docs[spec])) {
			fmt.Fprintf(&b, "%q: %q,\n", field, docs[spec][field])
		}

		b.WriteString("},\n")
	}

	b.WriteString("}\n")

	return format.Source([]byte(b.String()))
}

// typeKey is a struct's key in [Docs]: its import path and its name, without
// the type arguments of an instantiated generic.
func typeKey(t reflect.Type) string {
	name, _, _ := strings.Cut(t.Name(), "[")

	return t.PkgPath() + "." + name
}

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

// jsonName is the property a field marshals to, and whether it is an embedded
// struct whose fields encoding/json promotes.
func jsonName(field *reflect.StructField) (string, bool) {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")

	if name == "" && field.Anonymous && derefType(field.Type).Kind() == reflect.Struct {
		return "", true
	}

	if name == "" {
		name = field.Name
	}

	return name, false
}

// DirectiveSpecs reads the type specs off the mcpdocs `//go:generate`
// directive in file, so a surface's freshness test extracts exactly what its
// generation did rather than a second list of the same types.
//
// The directive is the one that runs a command whose last path element is
// mcpdocs, however it is named: ./mcpdocs inside this module, or
// github.com/primandproper/platform-go/v15/mcptool/mcpdocs from another.
func DirectiveSpecs(file string) ([]string, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("mcptool: reading %s: %w", file, err)
	}

	for line := range strings.Lines(string(src)) {
		if !strings.HasPrefix(line, "//go:generate ") {
			continue
		}

		fields := strings.Fields(line)

		command := slices.IndexFunc(fields, isMCPDocs)
		if command < 0 {
			continue
		}

		var specs []string

		for _, field := range fields[command+1:] {
			if strings.Contains(field, "/") && !strings.HasPrefix(field, "-") && strings.Contains(path.Base(field), ".") &&
				!strings.HasSuffix(field, ".go") {
				specs = append(specs, field)
			}
		}

		return specs, nil
	}

	return nil, fmt.Errorf("mcptool: %s carries no mcpdocs go:generate directive", file)
}

// isMCPDocs reports whether a go:generate field names the mcpdocs command, with
// or without a version query.
func isMCPDocs(field string) bool {
	command, _, _ := strings.Cut(field, "@")

	return path.Base(command) == "mcpdocs"
}

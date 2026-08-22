package envvars

import (
	"context"
	goast "go/ast"
	"go/token"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	reflast "github.com/primandproper/platform-go/v13/reflection/ast"

	"github.com/codemodus/kace"
)

// constantSuffix is appended to every generated identifier, so that a constant
// reads as the name of a variable rather than as the value it configures.
const constantSuffix = "EnvVarKey"

// Variable is one environment variable that overrides a configuration field.
type Variable struct {
	// Name is the variable a deployment sets, Options.Prefix included.
	Name string

	// ConstantName is the identifier Generate emits for it. It is derived from
	// Name without the prefix, so that moving an application's prefix does not
	// rename every constant that refers to it.
	ConstantName string

	// Default is the envDefault declared on the field this variable overrides.
	// When the variable overrides more than one field, it is the default of the
	// first field found.
	Default string

	// FieldPaths are the struct paths this variable overrides, each rooted at
	// the type name of the configuration struct it was reached from —
	// "APIServiceConfig.Database.ReadConnection.Host". A variable reachable from
	// several configuration structs has one path per struct.
	FieldPaths []string

	// HasDefault distinguishes a field that declares an empty default from one
	// that declares no default at all. The two differ: an envDefault always
	// wins over a value the config file supplied, so a field that declares one
	// can never hold a file value.
	HasDefault bool
}

// Collect returns every environment variable that can override a field of one
// of the configuration structs opts names, sorted by name.
//
// It is the whole of what Generate knows, exported because the constants are
// not the only useful thing to build out of it: the same set is what a caller
// needs to write a .env example, or to refuse to start on a variable that
// carries the prefix and matches nothing.
//
//nolint:gocritic // hugeParam: Options is taken by value so a call site can write it as a literal; this runs once, at build time
func Collect(ctx context.Context, opts Options) ([]Variable, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}

	idx, err := buildIndex(ctx, &opts)
	if err != nil {
		return nil, err
	}

	roots, err := idx.roots(&opts)
	if err != nil {
		return nil, err
	}

	w := &walker{idx: idx, vars: map[string]*variable{}}
	for _, root := range roots {
		entry := idx.structs[root]
		w.walk(entry, "", entry.name, map[string]bool{}, map[string]bool{})
	}

	return w.variables(opts.Prefix)
}

// buildIndex parses the module rooted at opts.Dir and every dependency module
// opts names.
func buildIndex(ctx context.Context, opts *Options) (*index, error) {
	modulePath, err := reflast.GetModulePath(opts.Dir)
	if err != nil {
		return nil, errors.Wrapf(err, "reading the module path of %q", opts.Dir)
	}

	idx := newIndex()
	if err = idx.parseModule(opts.Dir, "", modulePath); err != nil {
		return nil, err
	}

	dependencyDirs, err := opts.dependencyDirs(ctx)
	if err != nil {
		return nil, err
	}

	// Sorted, because two modules declaring the same package key would otherwise
	// resolve to whichever was parsed last, and map order would decide which.
	for _, importPath := range slices.Sorted(maps.Keys(dependencyDirs)) {
		if err = idx.parseModule(dependencyDirs[importPath], importPath, modulePath); err != nil {
			return nil, err
		}
	}

	return idx, nil
}

// variable is what the walk accumulates for one environment variable, before
// the application's prefix and the Go identifier are put on it.
type variable struct {
	defaultValue string
	fieldPaths   []string
	hasDefault   bool
}

// walker accumulates variables across the configuration structs it is walked
// over, keyed by their name without the application prefix.
type walker struct {
	idx  *index
	vars map[string]*variable
}

// walk traverses one named struct type.
//
// visited keys on the type and the prefix it was reached under, so that a type
// shared by several configuration structs is walked once per distinct prefix
// rather than once per field that names it. Two fields of the same type under
// the same prefix override the same variables by definition, so the second
// contributes nothing but a second field path.
//
// ancestors is the chain of types currently being walked, and is what stops a
// self-referential configuration struct: visited cannot, because each turn
// around the cycle concatenates another prefix and so keys differently.
func (w *walker) walk(entry *structEntry, envPrefix, fieldPath string, visited, ancestors map[string]bool) {
	typeKey := entry.key()
	if ancestors[typeKey] {
		return
	}

	visitKey := typeKey + "|" + envPrefix
	if visited[visitKey] {
		return
	}

	visited[visitKey] = true
	ancestors[typeKey] = true

	defer delete(ancestors, typeKey)

	w.walkStruct(entry, entry.structType, envPrefix, fieldPath, visited, ancestors)
}

// walkStruct traverses the fields of a struct literal. It is separate from walk
// because a struct declared inline has fields to walk but no type to key on.
func (w *walker) walkStruct(entry *structEntry, structType *goast.StructType, envPrefix, fieldPath string, visited, ancestors map[string]bool) {
	for _, field := range structType.Fields.List {
		tag := ""
		if field.Tag != nil {
			tag = field.Tag.Value
		}

		for _, name := range fieldNames(field) {
			w.walkField(entry, field.Type, tag, name, envPrefix, fieldPath, visited, ancestors)
		}
	}
}

// walkField records what one field contributes and descends into it if it is a
// struct, mirroring what caarlos0/env will do with the same field: an unset-able
// or ignored field contributes nothing, an `env` tag names a variable, and a
// struct is recursed into whether or not it carries an `envPrefix`.
func (w *walker) walkField(entry *structEntry, fieldType goast.Expr, tag, name, envPrefix, fieldPath string, visited, ancestors map[string]bool) {
	if !token.IsExported(name) {
		// The parser cannot set an unexported field, so it never reads a
		// variable for one.
		return
	}

	path := fieldPath + "." + name

	if envName := reflast.GetTagValue(tag, "env"); envName != "" && envName != "-" {
		w.record(envPrefix+envName, path, tag)
	}

	if prefix, declared := reflast.LookupTag(tag, "envPrefix"); declared {
		envPrefix += prefix
	}

	if nested, isInline := reflast.InlineStruct(fieldType); isInline {
		w.walkStruct(entry, nested, envPrefix, path, visited, ancestors)

		return
	}

	ref, isNamed := reflast.ParseTypeRef(fieldType)
	if !isNamed {
		return
	}

	key, resolvable := indexKey(ref, entry.pkgKey, entry.imports)
	if !resolvable {
		return
	}

	if nested, found := w.idx.structs[key]; found {
		w.walk(nested, envPrefix, path, visited, ancestors)
	}
}

// record notes that name overrides the field at path. The first field found
// decides the default, since a variable that overrides several fields has only
// one value to give them.
func (w *walker) record(name, path, tag string) {
	v, found := w.vars[name]
	if !found {
		defaultValue, hasDefault := reflast.LookupTag(tag, "envDefault")
		v = &variable{defaultValue: defaultValue, hasDefault: hasDefault}
		w.vars[name] = v
	}

	if !slices.Contains(v.fieldPaths, path) {
		v.fieldPaths = append(v.fieldPaths, path)
	}
}

// variables renders what was accumulated, sorted by name.
func (w *walker) variables(prefix string) ([]Variable, error) {
	claimed := make(map[string]string, len(w.vars))
	collected := make([]Variable, 0, len(w.vars))

	for _, name := range slices.Sorted(maps.Keys(w.vars)) {
		v := w.vars[name]

		constantName := kace.Pascal(name) + constantSuffix
		if !token.IsIdentifier(constantName) || !token.IsExported(constantName) {
			return nil, errors.Newf("environment variable %q yields %q, which is not an exported Go identifier", prefix+name, constantName)
		}

		if other, taken := claimed[constantName]; taken {
			return nil, errors.Newf("environment variables %q and %q both yield the constant %q", prefix+other, prefix+name, constantName)
		}

		claimed[constantName] = name

		collected = append(collected, Variable{
			Name:         prefix + name,
			ConstantName: constantName,
			Default:      v.defaultValue,
			FieldPaths:   v.fieldPaths,
			HasDefault:   v.hasDefault,
		})
	}

	return collected, nil
}

// fieldNames returns the names a field is written under: those it declares, or
// for an embedded field the base name of the type it embeds. Blank fields are
// skipped, having nothing to configure.
func fieldNames(field *goast.Field) []string {
	if len(field.Names) == 0 {
		name := reflast.EmbeddedFieldName(field.Type)
		if name == "" {
			return nil
		}

		return []string{name}
	}

	names := make([]string, 0, len(field.Names))
	for _, ident := range field.Names {
		if ident.Name != "_" {
			names = append(names, ident.Name)
		}
	}

	return names
}

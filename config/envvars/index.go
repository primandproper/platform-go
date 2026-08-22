package envvars

import (
	goast "go/ast"
	"go/token"

	"github.com/primandproper/platform-go/v13/errors"
	reflast "github.com/primandproper/platform-go/v13/reflection/ast"
)

// structEntry is a struct declaration together with what resolving the types
// its fields name requires: the key of the package it was declared in, for
// unqualified references, and the imports of the file it was declared in, for
// qualified ones.
type structEntry struct {
	structType *goast.StructType
	imports    map[string]string
	pkgKey     string
	name       string
}

func (e *structEntry) key() string {
	return e.pkgKey + "." + e.name
}

// index is every struct declaration and every type union found in the modules
// that were parsed, keyed by "<package>.<TypeName>".
//
// A package's key is its module-relative directory for the module being
// generated for, and its full import path for a dependency. The two cannot
// collide: a module-relative directory never begins with a domain name.
type index struct {
	structs map[string]*structEntry
	unions  map[string][]string
}

func newIndex() *index {
	return &index{
		structs: map[string]*structEntry{},
		unions:  map[string][]string{},
	}
}

// parseModule walks the module rooted at dir and records everything it
// declares. pkgPrefix is the key prefix for its packages: empty for the module
// being generated for, whose packages key on their module-relative directory,
// and the module's import path for a dependency. modulePath is the path of the
// module being generated for, which is what makes an import classifiable as one
// of its own.
func (idx *index) parseModule(dir, pkgPrefix, modulePath string) error {
	return reflast.WalkModule(dir, func(file *goast.File, relDir string) error {
		idx.addFile(file, packageKey(pkgPrefix, relDir), modulePath)

		return nil
	})
}

// packageKey builds the index key prefix for one package.
func packageKey(pkgPrefix, relDir string) string {
	switch {
	case pkgPrefix == "":
		return relDir
	case relDir == ".":
		return pkgPrefix
	default:
		return pkgPrefix + "/" + relDir
	}
}

// addFile records the structs and type unions one file declares.
func (idx *index) addFile(file *goast.File, pkgKey, modulePath string) {
	imports := reflast.ResolveImports(file, modulePath)

	for _, decl := range file.Decls {
		genDecl, isGenDecl := decl.(*goast.GenDecl)
		if !isGenDecl || genDecl.Tok != token.TYPE {
			continue
		}

		for _, spec := range genDecl.Specs {
			typeSpec, isTypeSpec := spec.(*goast.TypeSpec)
			if !isTypeSpec {
				continue
			}

			key := pkgKey + "." + typeSpec.Name.Name

			switch declared := typeSpec.Type.(type) {
			case *goast.StructType:
				idx.structs[key] = &structEntry{structType: declared, imports: imports, pkgKey: pkgKey, name: typeSpec.Name.Name}
			case *goast.InterfaceType:
				if members, isUnion := unionMemberKeys(declared, pkgKey, imports); isUnion {
					idx.unions[key] = members
				}
			}
		}
	}
}

// unionMemberKeys resolves the members of a type-union constraint to index
// keys, in declaration order.
//
// A union with a member this package cannot key — a dot-imported type, one
// qualified by a package the declaring file does not import — is not a usable
// set of roots, so it is not recorded as a union at all. Recording it partially
// would be worse than not recording it: UnionKey exists to make the output
// complete by construction, and a union that quietly lost a member is exactly
// the incompleteness it is there to rule out.
func unionMemberKeys(iface *goast.InterfaceType, pkgKey string, imports map[string]string) ([]string, bool) {
	terms, isUnion := reflast.UnionTerms(iface)
	if !isUnion {
		return nil, false
	}

	members := make([]string, 0, len(terms))

	for i := range terms {
		key, resolvable := indexKey(terms[i], pkgKey, imports)
		if !resolvable {
			return nil, false
		}

		members = append(members, key)
	}

	return members, true
}

// indexKey resolves a written type reference to an index key, given the key of
// the package the reference was written in and that file's imports. It reports
// false for a qualified reference whose package was not imported by the file,
// which is what a dot import or a reference this package cannot see looks like
// from here.
func indexKey(ref reflast.TypeRef, pkgKey string, imports map[string]string) (string, bool) {
	if ref.Package == "" {
		return pkgKey + "." + ref.Name, true
	}

	importKey, found := imports[ref.Package]
	if !found {
		return "", false
	}

	return importKey + "." + ref.Name, true
}

// roots returns the index keys of the configuration structs to walk, in the
// order they were declared.
func (idx *index) roots(opts *Options) ([]string, error) {
	keys := opts.Roots

	if opts.UnionKey != "" {
		members, found := idx.unions[opts.UnionKey]
		if !found {
			return nil, errors.Newf("no type union named %q was found; it is what UnionKey must name", opts.UnionKey)
		}

		keys = members
	}

	for _, key := range keys {
		if _, found := idx.structs[key]; !found {
			return nil, errors.Newf("configuration struct %q was named but not found by the parser", key)
		}
	}

	return keys, nil
}

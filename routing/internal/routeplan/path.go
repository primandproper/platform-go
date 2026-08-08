package routeplan

import (
	"fmt"
	"net/url"
	"reflect"
	"regexp"
)

// ParamSpec is a path parameter parsed out of a typed-path pattern such as
// "/users/{id:uint64}". Token is the resolved type token ("string" when the
// pattern omitted an annotation).
type ParamSpec struct {
	Name  string
	Token string
}

// pathParamRE matches a single path segment placeholder with an optional type
// annotation: {name} or {name:token}.
var pathParamRE = regexp.MustCompile(`\{([^:}/]+)(?::([^}/]+))?\}`)

// knownTokens is the set of supported type tokens. The value is unused; presence
// is what matters. Token → OpenAPI schema is derived by swaggest from the input
// struct field type, so this set only gates which annotations are legal and how a
// raw path value is parsed at runtime.
var knownTokens = map[string]struct{}{
	"string":  {},
	"slug":    {},
	"uuid":    {},
	"bool":    {},
	"int":     {},
	"int32":   {},
	"int64":   {},
	"uint":    {},
	"uint32":  {},
	"uint64":  {},
	"float":   {},
	"float64": {},
}

// ParsePath splits a typed-path pattern into a plain pattern (all type
// annotations stripped, safe to hand to any router) and the list of path
// parameters with their resolved tokens.
//
// An unknown token is returned as an error rather than a panic, because the two
// callers want different things from it. Registration turns it into a panic — a
// static programmer error surfaced at boot, matching how chi panics on a
// malformed pattern. A client call turns it into a returned error, because a
// consumer holding a descriptor from an imported package should not have a
// process taken down by a pattern it did not write.
func ParsePath(pattern string) (plain string, params []ParamSpec, err error) {
	plain = pathParamRE.ReplaceAllStringFunc(pattern, func(match string) string {
		sub := pathParamRE.FindStringSubmatch(match)
		name, token := sub[1], sub[2]
		if token == "" {
			token = "string"
		}

		if _, ok := knownTokens[token]; !ok {
			if err == nil {
				err = fmt.Errorf("unknown path parameter type %q in pattern %q", token, pattern)
			}

			return match
		}

		params = append(params, ParamSpec{Name: name, Token: token})

		return "{" + name + "}"
	})

	if err != nil {
		return "", nil, err
	}

	return plain, params, nil
}

// placeholderRE matches a placeholder in a plain (annotation-stripped) pattern.
var placeholderRE = regexp.MustCompile(`\{([^}/]+)\}`)

// FillPath substitutes values into a plain pattern's {name} placeholders,
// returning the path both unescaped and percent-escaped so a caller can hand
// both to net/url and let it decide which to put on the wire.
//
// Substitution is a single pass over the pattern: a value that happens to look
// like another placeholder is a value, not a placeholder, and is not substituted
// into again.
func FillPath(plain string, values map[string]string) (path, escaped string, err error) {
	fill := func(transform func(string) string) func(string) string {
		return func(match string) string {
			name := placeholderRE.FindStringSubmatch(match)[1]

			v, ok := values[name]
			if !ok {
				if err == nil {
					err = fmt.Errorf("no value for path parameter %q in pattern %q", name, plain)
				}

				return match
			}

			return transform(v)
		}
	}

	path = placeholderRE.ReplaceAllStringFunc(plain, fill(func(v string) string { return v }))
	escaped = placeholderRE.ReplaceAllStringFunc(plain, fill(url.PathEscape))

	if err != nil {
		return "", "", err
	}

	return path, escaped, nil
}

var textUnmarshalerType = reflect.TypeFor[TextUnmarshaler]()

// TokenMatchesType reports whether a path parameter's declared token is
// compatible with the Go type of the struct field it binds into. A type that
// parses itself (implements encoding.TextUnmarshaler, e.g. uuid.UUID or
// time.Time) is accepted for any token, since the type controls parsing.
func TokenMatchesType(token string, t reflect.Type) bool {
	t = DerefType(t)

	if reflect.PointerTo(t).Implements(textUnmarshalerType) {
		return true
	}

	switch token {
	case "string", "slug", "uuid":
		return t.Kind() == reflect.String
	case "bool":
		return t.Kind() == reflect.Bool
	case "int", "int32", "int64":
		return isIntKind(t.Kind())
	case "uint", "uint32", "uint64":
		return isUintKind(t.Kind())
	case "float", "float64":
		return isFloatKind(t.Kind())
	default:
		return false
	}
}

func isIntKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return true
	default:
		return false
	}
}

func isUintKind(k reflect.Kind) bool {
	switch k {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return true
	default:
		return false
	}
}

func isFloatKind(k reflect.Kind) bool {
	switch k {
	case reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}

// DerefType strips pointer indirection from a type.
func DerefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

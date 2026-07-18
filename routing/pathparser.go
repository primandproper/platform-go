package routing

import (
	"fmt"
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

// parsePath splits a typed-path pattern into a plain pattern (all type
// annotations stripped, safe to hand to any router) and the list of path
// parameters with their resolved tokens. It panics on an unknown token — a
// static programmer error surfaced at registration time, matching how chi
// panics on malformed patterns.
func parsePath(pattern string) (plain string, params []ParamSpec) {
	plain = pathParamRE.ReplaceAllStringFunc(pattern, func(match string) string {
		sub := pathParamRE.FindStringSubmatch(match)
		name, token := sub[1], sub[2]
		if token == "" {
			token = "string"
		}

		if _, ok := knownTokens[token]; !ok {
			panic(fmt.Sprintf("openapi: unknown path parameter type %q in pattern %q", token, pattern))
		}

		params = append(params, ParamSpec{Name: name, Token: token})

		return "{" + name + "}"
	})

	return plain, params
}

var textUnmarshalerType = reflect.TypeFor[textUnmarshaler]()

// tokenMatchesType reports whether a path parameter's declared token is
// compatible with the Go type of the struct field it binds into. A type that
// parses itself (implements encoding.TextUnmarshaler, e.g. uuid.UUID or
// time.Time) is accepted for any token, since the type controls parsing.
func tokenMatchesType(token string, t reflect.Type) bool {
	t = derefType(t)

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

func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return t
}

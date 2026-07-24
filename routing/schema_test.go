package routing_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v6/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// This file is a stress test for the OpenAPI schema the router generates. It
// registers routes whose input/output types exercise the full spread of Go
// shapes the reflector must handle — every basic scalar, aliases, stdlib
// specials (time.Time, []byte), pointers, slices, arrays, nested slices, maps
// (of scalars, structs, slices, and any), single- and multi-level nested
// structs, embedded (promoted) structs, named/defined types, interface fields,
// and excluded fields (json:"-" and unexported) — then asserts on the marshaled
// spec that each shape produced the expected JSON Schema.

// --- exercise types --------------------------------------------------------

type stressInner struct {
	Label string  `json:"label"`
	Score float64 `json:"score"`
}

// stressMiddle nests stressInner one level deeper so the reflector must emit a
// chain of $refs (AllTypes -> Middle -> Inner).
type stressMiddle struct {
	Inner  stressInner   `json:"inner"`
	Inners []stressInner `json:"inners"`
}

// stressEmbedded is embedded anonymously; its fields are promoted inline into
// the containing schema (no wrapper object, no $ref).
type stressEmbedded struct {
	EmbeddedField string `json:"embeddedField"`
}

// Named/defined scalar types: the reflector should still emit the underlying
// primitive schema.
type (
	stressStatus string
	stressCount  int
)

// stressAllTypes is the kitchen-sink request/response body.
type stressAllTypes struct {
	Time      time.Time              `json:"time"`
	Any       any                    `json:"any"`
	MapScalar map[string]int         `json:"mapScalar"`
	PtrBool   *bool                  `json:"ptrBool"`
	MapAny    map[string]any         `json:"mapAny"`
	MapSlice  map[string][]string    `json:"mapSlice"`
	MapStruct map[string]stressInner `json:"mapStruct"`
	PtrInt    *int                   `json:"ptrInt"`
	PtrTime   *time.Time             `json:"ptrTime"`
	PtrStruct *stressInner           `json:"ptrStruct"`
	PtrString *string                `json:"ptrString"`
	stressEmbedded
	String      string `json:"string"`
	unexported  string
	Status      stressStatus   `json:"status"`
	Ignored     string         `json:"-"`
	Deep        stressMiddle   `json:"deep"`
	IntSlice    []int          `json:"intSlice"`
	StructSlice []stressInner  `json:"structSlice"`
	Nested      stressInner    `json:"nested"`
	Bytes       []byte         `json:"bytes"`
	NestedSlice [][]string     `json:"nestedSlice"`
	PtrSlice    []*stressInner `json:"ptrSlice"`
	StringSlice []string       `json:"stringSlice"`
	Array       [4]int         `json:"array"`
	Float64     float64        `json:"float64"`
	Uint        uint           `json:"uint"`
	Int         int            `json:"int"`
	Int64       int64          `json:"int64"`
	Count       stressCount    `json:"count"`
	Uint64      uint64         `json:"uint64"`
	Float32     float32        `json:"float32"`
	Uint32      uint32         `json:"uint32"`
	Int32       int32          `json:"int32"`
	Rune        rune           `json:"rune"`
	Uint16      uint16         `json:"uint16"`
	Int16       int16          `json:"int16"`
	Int8        int8           `json:"int8"`
	Bool        bool           `json:"bool"`
	Uint8       uint8          `json:"uint8"`
	Byte        byte           `json:"byte"`
}

// stressParams exercises every parameter location the router understands.
type stressParams struct {
	Q      string `query:"q"`
	Header string `header:"X-Thing"`
	Cookie string `cookie:"sid"`
	ID     uint64 `path:"id"`
	Limit  int    `query:"limit"`
	Active bool   `query:"active"`
}

// --- JSON navigation helpers ----------------------------------------------

// specDoc marshals the router's spec and unmarshals it into a generic tree so
// tests can assert on the wire shape rather than swaggest's internal structs.
func specDoc(t *testing.T, r *routing.Router) map[string]any {
	t.Helper()

	must.NoError(t, r.Err())

	raw, err := r.MarshalSpec()
	must.NoError(t, err)

	var doc map[string]any
	must.NoError(t, json.Unmarshal(raw, &doc))

	return doc
}

// dig walks a chain of string map keys, failing the test if any segment is
// missing or is not an object.
func dig(t *testing.T, m map[string]any, keys ...string) map[string]any {
	t.Helper()

	cur := m
	for _, k := range keys {
		v, ok := cur[k]
		must.True(t, ok, must.Sprintf("missing key %q", k))
		cur, ok = v.(map[string]any)
		must.True(t, ok, must.Sprintf("key %q is not an object", k))
	}

	return cur
}

// refName returns the component name a $ref points at, e.g.
// "#/components/schemas/RoutingTestStressInner" -> "RoutingTestStressInner".
func refName(t *testing.T, schema map[string]any) string {
	t.Helper()

	ref, ok := schema["$ref"].(string)
	must.True(t, ok, must.Sprintf("schema is not a $ref: %v", schema))

	i := len(ref) - 1
	for i >= 0 && ref[i] != '/' {
		i--
	}

	return ref[i+1:]
}

// str reads a string-valued key.
func str(t *testing.T, m map[string]any, key string) string {
	t.Helper()

	v, ok := m[key].(string)
	must.True(t, ok, must.Sprintf("key %q is not a string (got %T)", key, m[key]))

	return v
}

// schemas returns the components/schemas object.
func schemas(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()

	return dig(t, doc, "components", "schemas")
}

// prop returns one property schema of a component by (component, property).
func prop(t *testing.T, comps map[string]any, comp, name string) map[string]any {
	t.Helper()

	props := dig(t, comps, comp, "properties")
	v, ok := props[name].(map[string]any)
	must.True(t, ok, must.Sprintf("component %q has no property %q", comp, name))

	return v
}

// --- the stress test -------------------------------------------------------

func TestSchema_AllTypes(T *testing.T) {
	T.Parallel()

	r := buildTestRouter(T)
	routing.Post(r, "/stress", func(_ context.Context, in stressAllTypes) (stressAllTypes, error) {
		return in, nil
	})

	doc := specDoc(T, r)
	comps := schemas(T, doc)

	// Resolve the request body's component name by following its $ref, rather
	// than hard-coding swaggest's derived name.
	reqSchema := dig(T, doc, "paths", "/stress", "post", "requestBody", "content", "application/json", "schema")
	body := refName(T, reqSchema)

	// field is a shorthand for "a property of the request body component".
	field := func(name string) map[string]any {
		return prop(T, comps, body, name)
	}

	T.Run("booleans and strings", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "boolean", str(t, prop(t, comps, body, "bool"), "type"))
		test.EqOp(t, "string", str(t, prop(t, comps, body, "string"), "type"))
	})

	T.Run("signed integers", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"int", "int8", "int16", "int32", "int64"} {
			f := field(name)
			test.EqOp(t, "integer", str(t, f, "type"), test.Sprintf("field %q", name))
			// signed ints carry no minimum
			test.MapNotContainsKey(t, f, "minimum", test.Sprintf("field %q", name))
		}
	})

	T.Run("unsigned integers get minimum 0", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"uint", "uint8", "uint16", "uint32", "uint64", "byte"} {
			f := field(name)
			test.EqOp(t, "integer", str(t, f, "type"), test.Sprintf("field %q", name))
			minimum, ok := f["minimum"].(float64)
			must.True(t, ok, must.Sprintf("field %q missing numeric minimum", name))
			test.EqOp(t, 0.0, minimum, test.Sprintf("field %q", name))
		}
	})

	T.Run("builtin aliases", func(t *testing.T) {
		t.Parallel()

		// rune == int32 -> integer/int32
		r := field("rune")
		test.EqOp(t, "integer", str(t, r, "type"))
		test.EqOp(t, "int32", str(t, r, "format"))
		// byte == uint8 -> covered by the unsigned-int subtest
	})

	T.Run("floats carry a format", func(t *testing.T) {
		t.Parallel()

		f32 := field("float32")
		test.EqOp(t, "number", str(t, f32, "type"))
		test.EqOp(t, "float", str(t, f32, "format"))

		f64 := field("float64")
		test.EqOp(t, "number", str(t, f64, "type"))
		test.EqOp(t, "double", str(t, f64, "format"))
	})

	T.Run("byte slice is a base64 string", func(t *testing.T) {
		t.Parallel()

		b := field("bytes")
		test.EqOp(t, "string", str(t, b, "type"))
		test.EqOp(t, "base64", str(t, b, "format"))
		// crucially NOT an array
		test.MapNotContainsKey(t, b, "items")
	})

	T.Run("time.Time is a date-time string", func(t *testing.T) {
		t.Parallel()

		tm := field("time")
		test.EqOp(t, "string", str(t, tm, "type"))
		test.EqOp(t, "date-time", str(t, tm, "format"))
	})

	T.Run("scalar pointers are nullable", func(t *testing.T) {
		t.Parallel()

		cases := map[string]string{
			"ptrString": "string",
			"ptrInt":    "integer",
			"ptrBool":   "boolean",
		}
		for name, wantType := range cases {
			f := field(name)
			test.EqOp(t, wantType, str(t, f, "type"), test.Sprintf("field %q", name))
			test.True(t, f["nullable"] == true, test.Sprintf("field %q should be nullable", name))
		}

		// *time.Time keeps its format and is nullable.
		pt := field("ptrTime")
		test.EqOp(t, "string", str(t, pt, "type"))
		test.EqOp(t, "date-time", str(t, pt, "format"))
		test.True(t, pt["nullable"] == true)
	})

	T.Run("pointer to struct is a ref", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "RoutingTestStressInner", refName(t, field("ptrStruct")))
	})

	T.Run("slices of scalars", func(t *testing.T) {
		t.Parallel()

		ss := field("stringSlice")
		test.EqOp(t, "array", str(t, ss, "type"))
		items, ok := ss["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "string", str(t, items, "type"))
		test.True(t, ss["nullable"] == true)

		is := field("intSlice")
		test.EqOp(t, "array", str(t, is, "type"))
		iItems, ok := is["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "integer", str(t, iItems, "type"))
	})

	T.Run("slices of structs and pointers ref the element", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"structSlice", "ptrSlice"} {
			f := field(name)
			test.EqOp(t, "array", str(t, f, "type"), test.Sprintf("field %q", name))
			items, ok := f["items"].(map[string]any)
			must.True(t, ok, must.Sprintf("field %q missing items", name))
			test.EqOp(t, "RoutingTestStressInner", refName(t, items), test.Sprintf("field %q", name))
		}
	})

	T.Run("nested slice is an array of arrays", func(t *testing.T) {
		t.Parallel()

		ns := field("nestedSlice")
		test.EqOp(t, "array", str(t, ns, "type"))
		outer, ok := ns["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "array", str(t, outer, "type"))
		inner, ok := outer["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "string", str(t, inner, "type"))
	})

	T.Run("fixed array is an array of the element type", func(t *testing.T) {
		t.Parallel()

		a := field("array")
		test.EqOp(t, "array", str(t, a, "type"))
		items, ok := a["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "integer", str(t, items, "type"))
	})

	T.Run("maps become objects with additionalProperties", func(t *testing.T) {
		t.Parallel()

		// map[string]int
		ms := field("mapScalar")
		test.EqOp(t, "object", str(t, ms, "type"))
		ap, ok := ms["additionalProperties"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "integer", str(t, ap, "type"))

		// map[string]stressInner -> additionalProperties is a $ref
		mst := field("mapStruct")
		test.EqOp(t, "object", str(t, mst, "type"))
		apStruct, ok := mst["additionalProperties"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "RoutingTestStressInner", refName(t, apStruct))

		// map[string][]string -> additionalProperties is an array
		msl := field("mapSlice")
		apSlice, ok := msl["additionalProperties"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "array", str(t, apSlice, "type"))

		// map[string]any -> additionalProperties is the empty (any) schema
		ma := field("mapAny")
		test.EqOp(t, "object", str(t, ma, "type"))
		apAny, ok := ma["additionalProperties"].(map[string]any)
		must.True(t, ok)
		test.MapLen(t, 0, apAny) // {} == accepts anything
	})

	T.Run("nested structs produce a ref chain", func(t *testing.T) {
		t.Parallel()

		// single level
		test.EqOp(t, "RoutingTestStressInner", refName(t, field("nested")))

		// multi level: body.deep -> Middle, Middle.inner -> Inner
		middle := refName(t, field("deep"))
		test.EqOp(t, "RoutingTestStressMiddle", middle)
		test.EqOp(t, "RoutingTestStressInner", refName(t, prop(t, comps, middle, "inner")))

		innerItems, ok := prop(t, comps, middle, "inners")["items"].(map[string]any)
		must.True(t, ok)
		test.EqOp(t, "RoutingTestStressInner", refName(t, innerItems))

		// the referenced Inner component really has the expected leaf props
		test.EqOp(t, "string", str(t, prop(t, comps, "RoutingTestStressInner", "label"), "type"))
		score := prop(t, comps, "RoutingTestStressInner", "score")
		test.EqOp(t, "number", str(t, score, "type"))
		test.EqOp(t, "double", str(t, score, "format"))
	})

	T.Run("embedded struct fields are promoted inline", func(t *testing.T) {
		t.Parallel()

		// embeddedField appears directly on the body component, not behind a ref
		// to a stressEmbedded component.
		ef := field("embeddedField")
		test.EqOp(t, "string", str(t, ef, "type"))
		test.MapNotContainsKey(t, comps, "RoutingTestStressEmbedded")
	})

	T.Run("named scalar types keep the underlying primitive", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "string", str(t, field("status"), "type"))
		test.EqOp(t, "integer", str(t, field("count"), "type"))
	})

	T.Run("interface field is an open schema", func(t *testing.T) {
		t.Parallel()

		// `any` reflects to {} — no type constraint at all.
		test.MapNotContainsKey(t, field("any"), "type")
	})

	T.Run("excluded fields never appear", func(t *testing.T) {
		t.Parallel()

		props := dig(t, comps, body, "properties")
		test.MapNotContainsKey(t, props, "ignored") // json:"-"
		test.MapNotContainsKey(t, props, "Ignored")
		test.MapNotContainsKey(t, props, "unexported")
	})
}

func TestSchema_Parameters(T *testing.T) {
	T.Parallel()

	r := buildTestRouter(T)
	routing.Get(r, "/things/{id:uint64}", func(_ context.Context, _ stressParams) (stressInner, error) {
		return stressInner{}, nil
	}, routing.WithEnvelope(false))

	doc := specDoc(T, r)

	op := dig(T, doc, "paths", "/things/{id}", "get")
	rawParams, isSlice := op["parameters"].([]any)
	must.True(T, isSlice)
	test.SliceLen(T, 6, rawParams)

	// index the parameter list by name for order-independent assertions.
	byName := map[string]map[string]any{}
	for _, p := range rawParams {
		pm, isObj := p.(map[string]any)
		must.True(T, isObj)
		byName[str(T, pm, "name")] = pm
	}

	T.Run("locations", func(t *testing.T) {
		t.Parallel()

		want := map[string]string{
			"id":      "path",
			"q":       "query",
			"limit":   "query",
			"active":  "query",
			"sid":     "cookie",
			"X-Thing": "header",
		}
		for name, in := range want {
			p, found := byName[name]
			must.True(t, found, must.Sprintf("missing parameter %q", name))
			test.EqOp(t, in, str(t, p, "in"), test.Sprintf("param %q", name))
		}
	})

	T.Run("path parameter is required", func(t *testing.T) {
		t.Parallel()

		test.True(t, byName["id"]["required"] == true)
		// non-path params are not marked required
		test.MapNotContainsKey(t, byName["q"], "required")
	})

	T.Run("parameter schemas reflect field types", func(t *testing.T) {
		t.Parallel()

		idSchema := dig(t, byName["id"], "schema")
		test.EqOp(t, "integer", str(t, idSchema, "type"))
		test.EqOp(t, 0.0, idSchema["minimum"]) // uint64

		limitSchema := dig(t, byName["limit"], "schema")
		test.EqOp(t, "integer", str(t, limitSchema, "type"))

		activeSchema := dig(t, byName["active"], "schema")
		test.EqOp(t, "boolean", str(t, activeSchema, "type"))

		qSchema := dig(t, byName["q"], "schema")
		test.EqOp(t, "string", str(t, qSchema, "type"))
	})
}

func TestSchema_EnvelopeWrapsOutput(T *testing.T) {
	T.Parallel()

	r := buildTestRouter(T)

	// enveloped (default) response wraps Out in APIResponse; raw response refs Out
	// directly.
	routing.Post(r, "/wrapped", func(_ context.Context, _ stressInner) (stressInner, error) {
		return stressInner{}, nil
	})
	routing.Post(r, "/raw", func(_ context.Context, _ stressInner) (stressInner, error) {
		return stressInner{}, nil
	}, routing.WithEnvelope(false))

	doc := specDoc(T, r)
	comps := schemas(T, doc)

	wrapped := refName(T, dig(T, doc,
		"paths", "/wrapped", "post", "responses", "201", "content", "application/json", "schema"))
	// the envelope component has a `data` property that refs the real output type.
	test.EqOp(T, "RoutingTestStressInner", refName(T, prop(T, comps, wrapped, "data")))

	raw := refName(T, dig(T, doc,
		"paths", "/raw", "post", "responses", "201", "content", "application/json", "schema"))
	test.EqOp(T, "RoutingTestStressInner", raw)
}

// referenced so the unexported stress field is not flagged unused; its presence
// is what exercises the reflector's unexported-field skip.
var _ = func() bool {
	var v stressAllTypes
	_ = v.unexported

	return true
}()

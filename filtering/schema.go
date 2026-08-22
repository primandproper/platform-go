package filtering

import (
	"encoding/json"

	platformerrors "github.com/primandproper/platform-go/v12/errors"

	"github.com/swaggest/jsonschema-go"
)

// maxResponseSizeProperty is the schema property PrepareJSONSchema writes the
// page-size ceiling onto. It is the `json` name of QueryFilter.MaxResponseSize,
// and PrepareJSONSchema fails loudly rather than silently if the two ever stop
// agreeing.
const maxResponseSizeProperty = "maxResponseSize"

// The reflector looks for this on the value it was handed and on a pointer to
// it, so a value receiver serves both.
var _ jsonschema.Preparer = QueryFilter{}

// PrepareJSONSchema writes the current page-size ceiling into every reflection
// of this type.
//
// MaxQueryFilterLimit is a var, so `maximum` cannot be a struct tag: a tag is
// fixed when this package compiles and the ceiling is not, so a service that
// raised it would go on publishing 250 to every client generated off this type
// while clamping somewhere else. This is the hook a swaggest reflector calls
// once it has built the object schema, which makes it the one place the bound
// is written — for QueryFilterSchema here, and equally for the openapi-go
// reflector routing runs over a consumer's own request and response types,
// where this package has no call of its own to patch afterwards.
//
// A missing property is an error rather than a silent no-op. It can only mean
// the `json` tag was renamed out from under this function, and an unbounded
// MaxResponseSize published to a generated client is exactly the failure the
// schema is reflected to avoid — so it surfaces as a panic out of
// QueryFilterSchema and a registration error out of routing, either of which
// TestQueryFilterSchema_Bounds catches first.
func (QueryFilter) PrepareJSONSchema(schema *jsonschema.Schema) error {
	property, ok := schema.Properties[maxResponseSizeProperty]
	if !ok || property.TypeObject == nil {
		return platformerrors.Newf("filtering: QueryFilter has no %q property to bound", maxResponseSizeProperty)
	}

	property.TypeObject.WithMaximum(float64(MaxQueryFilterLimit))

	return nil
}

// queryFilterSchemaJSON is the reflected schema, marshaled.
//
// It is deliberately not cached, and that is a change from when it was. The
// document was fixed when the package compiled right up until the page-size
// ceiling became something a consumer can set; caching it now would freeze
// whichever ceiling happened to be in place the first time anybody asked, which
// is the drift PrepareJSONSchema exists to prevent, reintroduced one layer up.
// Nothing calls this per request — a tool definition or an OpenAPI document is
// built once, at startup — so what it costs is paid where it is not measured.
//
// InlineRefs is asked for rather than relied on. QueryFilter has no field the
// reflector would name today, but a $ref/$defs document is the one thing a tool
// definition cannot carry — a provider is handed the map unchanged and resolves
// nothing — so the option is here to keep a future nested field from turning a
// usable schema into a broken one at the far end of an API call.
func queryFilterSchemaJSON() []byte {
	reflector := jsonschema.Reflector{}

	schema, err := reflector.Reflect(QueryFilter{}, jsonschema.InlineRefs)
	if err != nil {
		panic("filtering: reflecting QueryFilter's JSON Schema: " + err.Error())
	}

	raw, err := json.Marshal(schema)
	if err != nil {
		panic("filtering: marshaling QueryFilter's JSON Schema: " + err.Error())
	}

	return raw
}

// QueryFilterSchema returns the JSON Schema for QueryFilter as a decoded
// document — the shape llm.Tool.Schema takes, which is also the shape an MCP
// tool definition takes, and the same object the OpenAPI spec describes this
// type with.
//
// It is reflected off the struct rather than written out beside it, and that is
// why it lives here. A hand-written mirror of this type is a second copy that
// can be wrong: one such mirror described SortBy as the field to sort by rather
// than the direction to sort in, declared MaxResponseSize as an unbounded
// integer, and keyed on Go field names against camelCase tags. None of that was
// a mistake when it was written. The struct moved and the mirror did not, and
// nothing anywhere said so.
//
// Everything the document asserts beyond the field types is a struct tag on
// QueryFilter — bar the page-size ceiling, which PrepareJSONSchema writes out
// of MaxQueryFilterLimit because that one is a var and a tag cannot follow one.
// Either way the constraints are written once for every reflector that reads
// them. The schema is therefore about the type and not about any one use of it:
// nothing here is MCP-shaped, or HTTP-shaped, and a caller wanting a filter
// described to a model and a caller generating a client both get this.
//
// The map is freshly decoded on every call and the caller owns it outright.
// Merging these properties into a larger tool input, dropping the ones an
// endpoint does not honor, or tightening a bound is editing a private copy —
// which is the point, since a shared one would have every tool definition in a
// process editing the same document.
//
// It panics if QueryFilter does not reflect. That can only be a malformed tag
// in this package or a PrepareJSONSchema that no longer recognizes the field it
// bounds, both of them properties of a type this package owns and both caught
// by TestQueryFilterSchema before they ship; an error return would put an
// impossible branch at every call site instead, and a tool registry would spend
// its own error path on it forever.
func QueryFilterSchema() map[string]any {
	var doc map[string]any

	// These bytes came out of json.Marshal, so either this decodes or nothing
	// does.
	if err := json.Unmarshal(queryFilterSchemaJSON(), &doc); err != nil {
		panic("filtering: decoding QueryFilter's JSON Schema: " + err.Error())
	}

	return doc
}

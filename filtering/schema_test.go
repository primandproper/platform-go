package filtering

import (
	"encoding/json"
	"maps"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi3"
)

// The schema is reflected off QueryFilter, so most of what could go wrong with
// it is a tag that says the wrong thing rather than code that computes the wrong
// thing. These tests are written against the constants and the struct itself for
// that reason: a tag holds "50" because a tag cannot hold
// DefaultQueryFilterLimit, and the assertions below are what stand in for the
// compiler noticing.
//
// The page-size ceiling is the exception, and is tested the other way round.
// MaxQueryFilterLimit is a var, so there is no tag to drift — PrepareJSONSchema
// writes the bound out of the var — and what has to be checked instead is that
// it is written at all, and that it is still written after somebody changes it.
// TestMaxQueryFilterLimit_Override is that.

// jsonNames is every property name QueryFilter's tags declare, in field order.
// It fails on an exported field with no `json` tag rather than skipping it,
// because such a field is one encoding/json would key on its Go name and the
// schema would not mention at all.
func jsonNames(t *testing.T) []string {
	t.Helper()

	typ := reflect.TypeFor[QueryFilter]()

	names := make([]string, 0, typ.NumField())

	for field := range typ.Fields() {
		tag, tagged := field.Tag.Lookup("json")
		if !tagged {
			if field.IsExported() {
				t.Fatalf("QueryFilter.%s is exported and has no json tag", field.Name)
			}

			continue
		}

		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}

		names = append(names, name)
	}

	return names
}

// property reads one property subschema, failing if it is absent or is not an
// object.
func property(t *testing.T, name string) map[string]any {
	t.Helper()

	properties, ok := QueryFilterSchema()["properties"].(map[string]any)
	must.True(t, ok, must.Sprint("schema has no properties object"))

	prop, ok := properties[name].(map[string]any)
	must.True(t, ok, must.Sprintf("schema has no property %q", name))

	return prop
}

func TestQueryFilterSchema(t *testing.T) {
	t.Parallel()

	schema := QueryFilterSchema()

	test.EqOp(t, "object", schema["type"])

	// The struct is closed: encoding/json discards a key it does not know, so a
	// model that invents one is answered with silence rather than an error.
	test.EqOp(t, false, schema["additionalProperties"])

	description, ok := schema["description"].(string)
	test.True(t, ok, test.Sprint("schema has no description"))
	test.NotEq(t, "", description)
}

func TestQueryFilterSchema_PropertiesAreTheTagNames(t *testing.T) {
	t.Parallel()

	properties, ok := QueryFilterSchema()["properties"].(map[string]any)
	must.True(t, ok, must.Sprint("schema has no properties object"))

	want := jsonNames(t)
	got := slices.Sorted(maps.Keys(properties))

	slices.Sort(want)

	// Go field names decode today only because encoding/json falls back to a
	// case-insensitive match against the tag. The keys are the tags so that the
	// fallback is not what the schema is resting on.
	test.Eq(t, want, got)
}

func TestQueryFilterSchema_EveryPropertyIsDescribed(t *testing.T) {
	t.Parallel()

	// A property with no description is one a model has only its name to go on
	// for, which is how "sortBy" gets a column name put in it.
	for _, name := range jsonNames(t) {
		prop := property(t, name)

		description, ok := prop["description"].(string)
		test.True(t, ok, test.Sprintf("%s has no description", name))
		test.NotEq(t, "", description)
	}
}

func TestQueryFilterSchema_NothingIsNullable(t *testing.T) {
	t.Parallel()

	// Every field is a pointer, which the reflector reads as nullable unless
	// told otherwise. It is not: absent is how a filter says it does not filter
	// on something, and null is never emitted for one.
	for _, name := range jsonNames(t) {
		test.NotEq(t, "null", property(t, name)["type"])
	}
}

func TestQueryFilterSchema_SortByIsADirection(t *testing.T) {
	t.Parallel()

	prop := property(t, "sortBy")

	test.EqOp(t, "string", prop["type"])

	// Read off the vars rather than spelled out: renaming a direction has to
	// fail here rather than ship a schema offering one that FromParams rejects.
	enum, ok := prop["enum"].([]any)
	must.True(t, ok, must.Sprint("sortBy has no enum"))
	test.Eq(t, []any{*SortAscending, *SortDescending}, enum)

	fallback, ok := prop["default"].(string)
	must.True(t, ok, must.Sprint("sortBy has no default"))
	test.EqOp(t, *DefaultQueryFilter().SortBy, fallback)
}

func TestQueryFilterSchema_Bounds(t *testing.T) {
	t.Parallel()

	prop := property(t, "maxResponseSize")

	test.EqOp(t, "integer", prop["type"])

	// The minimum and the default are tags, spelled out because a struct tag
	// cannot name a constant. These are the assertions that keep the two
	// spellings the same number. The maximum is not a tag at all, and comes
	// from PrepareJSONSchema.
	minimum, ok := prop["minimum"].(float64)
	must.True(t, ok, must.Sprint("maxResponseSize has no minimum"))
	test.EqOp(t, float64(0), minimum)

	maximum, ok := prop["maximum"].(float64)
	must.True(t, ok, must.Sprint("maxResponseSize has no maximum"))
	test.EqOp(t, float64(MaxQueryFilterLimit), maximum)

	size, ok := prop["default"].(float64)
	must.True(t, ok, must.Sprint("maxResponseSize has no default"))
	test.EqOp(t, float64(DefaultQueryFilterLimit), size)
	test.EqOp(t, float64(*DefaultQueryFilter().MaxResponseSize), size)

	// The ceiling is a clamp and not a rejection, which no JSON Schema keyword
	// can say. The description is where it gets said, so it has to keep saying
	// it.
	description, ok := prop["description"].(string)
	must.True(t, ok, must.Sprint("maxResponseSize has no description"))
	test.StrContains(t, description, "clamped")
}

func TestQueryFilterSchema_TimeWindows(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"createdAfter", "createdBefore", "updatedAfter", "updatedBefore"} {
		prop := property(t, name)

		test.EqOp(t, "string", prop["type"])
		test.EqOp(t, "date-time", prop["format"], test.Sprintf("%s is not a timestamp", name))
	}
}

func TestQueryFilterSchema_CallerOwnsTheMap(t *testing.T) {
	t.Parallel()

	// A tool definition merging these properties into a larger input, or
	// dropping the ones its endpoint ignores, edits its own copy. One shared map
	// would have the first such caller decide what every later one sees.
	first := QueryFilterSchema()
	first["type"] = "clobbered"
	delete(first, "properties")

	second := QueryFilterSchema()
	test.EqOp(t, "object", second["type"])
	test.MapNotEmpty(t, second["properties"].(map[string]any))
}

// TestQueryFilterSchema_KeysRoundTrip builds a document out of the schema's own
// property names and decodes it, which is the end of the drift this schema
// exists to stop: a key the schema names that QueryFilter does not read arrives
// as a filter silently not applied.
func TestQueryFilterSchema_KeysRoundTrip(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

	document := map[string]any{
		"sortBy":          *SortDescending,
		"createdAfter":    stamp,
		"createdBefore":   stamp,
		"updatedAfter":    stamp,
		"updatedBefore":   stamp,
		"maxResponseSize": MaxQueryFilterLimit,
		"includeArchived": true,
		"cursor":          "cursor_01HZY0000000000000",
	}

	// Every property the schema declares is exercised, so a field added to the
	// struct without a line here fails rather than going untested.
	test.SliceLen(t, len(document), jsonNames(t))

	for _, name := range jsonNames(t) {
		_, ok := document[name]
		test.True(t, ok, test.Sprintf("schema declares %q and this test does not send it", name))
	}

	raw, err := json.Marshal(document)
	must.NoError(t, err)

	var qf QueryFilter
	must.NoError(t, json.Unmarshal(raw, &qf))

	must.NotNil(t, qf.SortBy)
	test.EqOp(t, *SortDescending, *qf.SortBy)

	must.NotNil(t, qf.CreatedAfter)
	test.EqOp(t, stamp, *qf.CreatedAfter)

	must.NotNil(t, qf.CreatedBefore)
	test.EqOp(t, stamp, *qf.CreatedBefore)

	must.NotNil(t, qf.UpdatedAfter)
	test.EqOp(t, stamp, *qf.UpdatedAfter)

	must.NotNil(t, qf.UpdatedBefore)
	test.EqOp(t, stamp, *qf.UpdatedBefore)

	must.NotNil(t, qf.MaxResponseSize)
	test.EqOp(t, MaxQueryFilterLimit, *qf.MaxResponseSize)

	must.NotNil(t, qf.IncludeArchived)
	test.EqOp(t, true, *qf.IncludeArchived)

	must.NotNil(t, qf.Cursor)
	test.EqOp(t, "cursor_01HZY0000000000000", *qf.Cursor)
}

// TestQueryFilterSchema_MarshalsBackToItself checks the direction the round-trip
// test does not: what a QueryFilter writes is keyed the way the schema says it
// reads.
func TestQueryFilterSchema_MarshalsBackToItself(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(DefaultQueryFilter())
	must.NoError(t, err)

	var written map[string]any
	must.NoError(t, json.Unmarshal(raw, &written))

	declared := jsonNames(t)

	for name := range written {
		test.True(t, slices.Contains(declared, name), test.Sprintf("marshaled key %q is not in the schema", name))
	}
}

// TestMaxQueryFilterLimit_Override is the ceiling being a var rather than a
// constant: a service that needs a bigger page than platform picked can have
// one, and gets it in the document it publishes as well as in the clamp.
//
// It is the only test in this package that writes a package-level var, and so
// the only one that does not call t.Parallel(). Every other test here reads
// MaxQueryFilterLimit, and the sequential phase — which finishes before any
// parallel test resumes — is the only place a write to it is not a race. The
// suite runs under -race, which is what keeps that claim honest.
//
//nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
func TestMaxQueryFilterLimit_Override(t *testing.T) {
	const raised = 512

	original := MaxQueryFilterLimit
	t.Cleanup(func() { MaxQueryFilterLimit = original })

	// Read the schema before raising the ceiling. A cached document would be
	// frozen by this call, and every assertion below would then be reading a
	// 250 that the clamp no longer applies — which is the whole reason the
	// reflection is not cached.
	before, ok := property(t, maxResponseSizeProperty)["maximum"].(float64)
	must.True(t, ok, must.Sprint("maxResponseSize has no maximum"))
	test.EqOp(t, float64(original), before)

	MaxQueryFilterLimit = raised

	t.Run("the clamp follows it", func(t *testing.T) { //nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
		test.EqOp(t, uint16(raised), ClampResponseSize(1000))

		// Still clamped before the narrowing, at the new ceiling as at the old.
		test.EqOp(t, uint16(raised), ClampResponseSize(70000))
		test.EqOp(t, uint16(raised), ClampResponseSize(math.MaxUint64))

		// And a value that was over the old ceiling is now simply a page size.
		test.EqOp(t, uint16(300), ClampResponseSize(300))
	})

	t.Run("every path that applies it follows it", func(t *testing.T) { //nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
		qf := &QueryFilter{}
		must.NoError(t, qf.FromParams(url.Values{QueryKeyLimit: []string{"1000"}}))
		must.NotNil(t, qf.MaxResponseSize)
		test.EqOp(t, uint16(raised), *qf.MaxResponseSize)

		set := &QueryFilter{}
		set.SetMaxResponseSize(1000)
		must.NotNil(t, set.MaxResponseSize)
		test.EqOp(t, uint16(raised), *set.MaxResponseSize)

		normalized := &QueryFilter{MaxResponseSize: new(uint16(400))}
		must.NoError(t, normalized.Normalize())
		test.EqOp(t, uint16(400), *normalized.MaxResponseSize)
	})

	t.Run("the published schema follows it", func(t *testing.T) { //nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
		maximum, found := property(t, maxResponseSizeProperty)["maximum"].(float64)
		must.True(t, found, must.Sprint("maxResponseSize has no maximum"))
		test.EqOp(t, float64(raised), maximum)
	})

	// The reason the bound is written by a Preparer rather than patched onto
	// QueryFilterSchema's map: a consumer's own response type carries a filter,
	// and it is reflected by routing's OpenAPI reflector, which this package
	// has no call of its own to patch afterwards.
	t.Run("a consumer's OpenAPI document follows it", func(t *testing.T) { //nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
		type listResponse struct {
			Pagination Pagination `json:"pagination"`
		}

		reflector := openapi3.NewReflector()

		oc, err := reflector.NewOperationContext(http.MethodGet, "/things")
		must.NoError(t, err)
		oc.SetID("listThings")
		oc.AddRespStructure(listResponse{}, openapi.WithHTTPStatus(http.StatusOK))
		must.NoError(t, reflector.AddOperation(oc))

		raw, err := reflector.Spec.MarshalJSON()
		must.NoError(t, err)

		var document map[string]any
		must.NoError(t, json.Unmarshal(raw, &document))

		schemas := componentSchemas(t, document)

		filter, found := schemas["FilteringQueryFilter"].(map[string]any)
		must.True(t, found, must.Sprintf("spec has no QueryFilter schema, only %v", slices.Sorted(maps.Keys(schemas))))

		properties, found := filter["properties"].(map[string]any)
		must.True(t, found, must.Sprint("QueryFilter schema has no properties"))

		size, found := properties[maxResponseSizeProperty].(map[string]any)
		must.True(t, found, must.Sprint("QueryFilter schema has no maxResponseSize"))

		maximum, found := size["maximum"].(float64)
		must.True(t, found, must.Sprint("maxResponseSize has no maximum in the OpenAPI document"))
		test.EqOp(t, float64(raised), maximum)
	})

	t.Run("lowering it works the same way", func(t *testing.T) { //nolint:paralleltest // mutates the package-level page-size ceiling; must run serially
		MaxQueryFilterLimit = 10

		test.EqOp(t, uint16(10), ClampResponseSize(1000))

		maximum, found := property(t, maxResponseSizeProperty)["maximum"].(float64)
		must.True(t, found, must.Sprint("maxResponseSize has no maximum"))
		test.EqOp(t, float64(10), maximum)
	})

	// Restored here rather than left to the Cleanup, because the point being
	// made is that the document is not frozen in either direction.
	MaxQueryFilterLimit = original

	after, ok := property(t, maxResponseSizeProperty)["maximum"].(float64)
	must.True(t, ok, must.Sprint("maxResponseSize has no maximum"))
	test.EqOp(t, float64(original), after)
}

// componentSchemas digs the reflected component schemas out of a marshaled
// OpenAPI document.
func componentSchemas(t *testing.T, document map[string]any) map[string]any {
	t.Helper()

	components, ok := document["components"].(map[string]any)
	must.True(t, ok, must.Sprint("spec has no components"))

	schemas, ok := components["schemas"].(map[string]any)
	must.True(t, ok, must.Sprint("spec has no component schemas"))

	return schemas
}

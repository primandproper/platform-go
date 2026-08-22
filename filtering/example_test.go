package filtering_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v12/filtering"
	"github.com/primandproper/platform-go/v12/llm"
)

// A tool that lists something takes a page of a collection as its input, which
// is what a QueryFilter is. Handing the model this rather than a description of
// it written out beside the tool is what keeps the two from drifting: the enum,
// the bounds, and the property names are the struct's own tags.
func ExampleQueryFilterSchema() {
	tool := llm.Tool{
		Name:        "list_recipes",
		Description: "List the caller's recipes.",
		Schema:      filtering.QueryFilterSchema(),
	}

	properties, _ := tool.Schema["properties"].(map[string]any)

	// A tool whose endpoint honors only some of these drops the rest. The map
	// is this caller's own copy, so doing that affects nobody else's.
	delete(properties, "includeArchived")

	sortBy, _ := properties["sortBy"].(map[string]any)
	size, _ := properties["maxResponseSize"].(map[string]any)

	fmt.Printf("%s: %s\n", tool.Name, tool.Description)
	fmt.Println("sortBy:", sortBy["enum"])
	fmt.Println("maxResponseSize:", size["minimum"], "to", size["maximum"], "defaulting to", size["default"])

	// Output:
	// list_recipes: List the caller's recipes.
	// sortBy: [asc desc]
	// maxResponseSize: 0 to 250 defaulting to 50
}

// A decoder for a wire format reaches its page size as something wider than a
// uint16, because no wire format has one: protobuf carries a uint32, JSON hands
// a decoder a number, a query parameter hands it a string. Narrowing that to the
// field's type before the ceiling is applied wraps rather than clamps, and the
// wrapped value is indistinguishable from one the client actually sent.
// SetMaxResponseSize takes the wide value, so there is no order left to get
// wrong.
func ExampleQueryFilter_SetMaxResponseSize() {
	// What a generated protobuf message hands a converter.
	var maxResponseSize uint32 = 70000

	qf := &filtering.QueryFilter{}
	qf.SetMaxResponseSize(uint64(maxResponseSize))

	// Narrowing first would have produced 4464, which Normalize then clamps to
	// 250 — a legible-looking page size nobody asked for, raised nowhere.
	fmt.Println("clamped first:", *qf.MaxResponseSize)
	fmt.Println("narrowed first:", uint16(maxResponseSize))

	// Output:
	// clamped first: 250
	// narrowed first: 4464
}

// The page-size ceiling is a var, so a service that pages cheaply is not held
// to the number platform picked. Set it during initialization — before the
// first filter is parsed and before any schema is reflected — and the clamp and
// the document the type publishes move together.
func ExampleMaxQueryFilterLimit() {
	defer func(original uint16) { filtering.MaxQueryFilterLimit = original }(filtering.MaxQueryFilterLimit)

	filtering.MaxQueryFilterLimit = 512

	// A client asking for a thousand rows is answered with the new ceiling
	// rather than platform's.
	fmt.Println(filtering.ClampResponseSize(1000))

	// And the schema a generated client or a tool-calling model is handed says
	// so, rather than going on promising 250 while the clamp allows 512.
	properties := filtering.QueryFilterSchema()["properties"].(map[string]any)
	maxResponseSize := properties["maxResponseSize"].(map[string]any)

	fmt.Println(maxResponseSize["maximum"])

	// Output:
	// 512
	// 512
}

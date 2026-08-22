package filtering_test

import (
	"fmt"

	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/llm"
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

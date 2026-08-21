package filtering_test

import (
	"database/sql"
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

// listRecipesParams stands in for the params struct a query generator emits.
// sqlc names these fields off the arguments in the .sql file, so a consumer's
// looks like this without platform having any say in it — which is why Bind
// hands back values to copy across rather than a type the generated struct
// would have to embed.
type listRecipesParams struct {
	CreatedAfter    sql.NullTime
	CreatedBefore   sql.NullTime
	UpdatedAfter    sql.NullTime
	UpdatedBefore   sql.NullTime
	BelongsToUser   string
	Cursor          sql.NullString
	ResultLimit     sql.NullInt32
	IncludeArchived sql.NullBool
}

// listRecipes stands in for the generated query method the params struct is
// handed to. A real one takes a context and a connection and returns rows; this
// one reports the window it was given.
func listRecipes(params *listRecipesParams) string {
	return fmt.Sprintf("owner=%s limit=%d includeArchived=%v createdAfter=%v",
		params.BelongsToUser,
		params.ResultLimit.Int32,
		params.IncludeArchived.Valid,
		params.CreatedAfter.Valid,
	)
}

// A list query binds its window from a filter, and the seven conversions that
// takes are the same seven every time. The arguments the query is keyed on are
// the caller's own — Bind does not know about them and does not touch them.
func ExampleBind() {
	filter := &filtering.QueryFilter{MaxResponseSize: new(uint16(1_000))}

	// A page size above the ceiling is answered with the ceiling rather than
	// rejected, and the clamp lands before the narrowing to the driver's type.
	// An unset field stays a NULL, which the emitted predicates coalesce to a
	// bound that admits everything.
	args := filtering.Bind(filter)

	fmt.Println(listRecipes(&listRecipesParams{
		CreatedAfter:    args.CreatedAfter,
		CreatedBefore:   args.CreatedBefore,
		UpdatedAfter:    args.UpdatedAfter,
		UpdatedBefore:   args.UpdatedBefore,
		Cursor:          args.Cursor,
		ResultLimit:     args.ResultLimit,
		IncludeArchived: args.IncludeArchived,
		BelongsToUser:   "user_001",
	}))

	// Output:
	// owner=user_001 limit=250 includeArchived=false createdAfter=false
}

// listRecipesRow stands in for the row a list query returns: the columns, plus
// the two windowed counts the same statement carried along so that the page and
// the numbers describing it come from one moment.
type listRecipesRow struct {
	ID            string
	Name          string
	FilteredCount int64
	TotalCount    int64
}

type recipe struct {
	ID   string
	Name string
}

// Turning those rows into the page an endpoint answers with is the other end of
// the same query. The conversion from a row to a domain type stays here,
// because that is the half that is genuinely about this table; the loop, the
// counts, and the cursor do not.
func ExampleDrain() {
	rows := []listRecipesRow{
		{ID: "recipe_001", Name: "gruel", FilteredCount: 2, TotalCount: 40},
		{ID: "recipe_002", Name: "porridge", FilteredCount: 2, TotalCount: 40},
	}

	page := filtering.Drain(
		rows,
		func(r listRecipesRow) *recipe { return &recipe{ID: r.ID, Name: r.Name} },
		func(r listRecipesRow) (filtered, total int64) { return r.FilteredCount, r.TotalCount },
		func(r *recipe) string { return r.ID },
		filtering.DefaultQueryFilter(),
	)

	filtered, total, known := page.Counts()

	fmt.Println("rows:", len(page.Data))
	fmt.Println("counts:", filtered, total, known)
	// The cursor reaching the next page is the last row's identifier. It is not
	// a "there is more" signal — the counts are what say that.
	fmt.Println("next cursor:", page.Cursor)

	// Output:
	// rows: 2
	// counts: 2 40 true
	// next cursor: recipe_002
}

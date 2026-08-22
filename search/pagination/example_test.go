package searchpagination_test

import (
	"context"
	"fmt"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	searchpagination "github.com/primandproper/platform-go/v13/search/pagination"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
)

// widgetSubset is what the index holds: the handful of fields worth searching,
// not the object a client asked for.
type widgetSubset struct {
	ID   string
	Name string
}

// widget is what the store hands back for a hit ID — the domain object, with
// the fields the index has no business carrying.
type widget struct {
	ID    string
	Name  string
	Price int
}

// exampleBackend names the backend that issues the cursors below. A cursor is
// only meaningful to the backend that issued it, which is why the tag is
// encoded into the token.
const exampleBackend = "example"

// exampleIndex stands in for elasticsearch.NewIndex or algolia.NewIndex: it
// answers one page of hits at a time and issues an opaque cursor for the next.
// Only the Search method matters here, which is textsearch.IndexSearcher.
type exampleIndex struct {
	err  error
	hits []*widgetSubset
}

func (i *exampleIndex) Search(_ context.Context, req textsearch.SearchRequest) (*textsearch.SearchResults[widgetSubset], error) {
	if i.err != nil {
		return nil, i.err
	}

	from, err := textsearch.DecodeCursor(exampleBackend, req.Cursor)
	if err != nil {
		return nil, err
	}

	// The limit the filter carried reaches the backend, so the page is the size
	// the caller asked for rather than whatever this backend defaults to.
	end := min(from+textsearch.EffectiveLimit(req.Limit, 0), len(i.hits))

	results := &textsearch.SearchResults[widgetSubset]{Hits: i.hits[from:end]}
	if end < len(i.hits) {
		if results.NextCursor, err = textsearch.EncodeCursor(exampleBackend, end); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// exampleStore stands in for the database. Its read-many is the hydrate
// argument below, and is the only half of the loop that differs between call
// sites.
type exampleStore struct {
	rows map[string]*widget
}

func (s *exampleStore) GetWidgetsWithIDs(_ context.Context, ids []string) ([]*widget, error) {
	found := make([]*widget, 0, len(ids))

	for _, id := range ids {
		if row, ok := s.rows[id]; ok {
			found = append(found, row)
		}
	}

	return found, nil
}

func newExampleIndex() *exampleIndex {
	return &exampleIndex{
		hits: []*widgetSubset{
			{ID: "w1", Name: "carrot"},
			{ID: "w2", Name: "carrot cake"},
			{ID: "w3", Name: "carrot soup"},
		},
	}
}

func newExampleStore() *exampleStore {
	return &exampleStore{
		rows: map[string]*widget{
			"w1": {ID: "w1", Name: "carrot", Price: 100},
			"w2": {ID: "w2", Name: "carrot cake", Price: 850},
			"w3": {ID: "w3", Name: "carrot soup", Price: 400},
		},
	}
}

// widgetID reads the ID off a hit. It is the half of Hydrated that knows the
// index's document shape; the store's read-many is the other half.
func widgetID(hit *widgetSubset) string { return hit.ID }

// Example runs a text search the way a list endpoint does: query the index,
// read the hit IDs back out of the store, and hand the client a page it can ask
// past. Both type parameters are inferred, so neither is spelled here.
func Example() {
	ctx := context.Background()

	store := newExampleStore()

	// The page size lives on the filter the client sent, which is the whole
	// reason there is no limit left to forget at this call site.
	filter := filtering.DefaultQueryFilter()
	filter.MaxResponseSize = new(uint16(2))

	page, err := searchpagination.Hydrated(ctx, newExampleIndex(), "carrot", filter,
		widgetID,
		store.GetWidgetsWithIDs,
	)
	if err != nil {
		panic(err)
	}

	for _, w := range page.Data {
		fmt.Println(w.Name, w.Price)
	}

	// The page carries the index's own token rather than the last row's ID, and a
	// total of zero, meaning unknown: the index reported that another page exists,
	// not how many results there are in all. Reporting the two rows above as the
	// total would tell the client this truncated page was the entire result set.
	fmt.Println("more pages:", page.Cursor != "")
	fmt.Println("total:", page.TotalCount)

	// Output:
	// carrot 100
	// carrot cake 850
	// more pages: true
	// total: 0
}

// Example_paging walks the result set to its end. The cursor a page carries
// goes back onto the next filter untouched — a client hands the index its own
// token, and nothing between the two reads it.
func Example_paging() {
	ctx := context.Background()

	var (
		index  = newExampleIndex()
		store  = newExampleStore()
		filter = filtering.DefaultQueryFilter()
	)

	filter.MaxResponseSize = new(uint16(2))

	for page := 1; ; page++ {
		// A filter with no cursor is a client asking for the first page, not a token
		// the index has to make sense of.
		fmt.Printf("page %d, resuming: %t\n", page, searchpagination.Resuming(filter))

		result, err := searchpagination.Hydrated(ctx, index, "carrot", filter, widgetID, store.GetWidgetsWithIDs)
		if err != nil {
			panic(err)
		}

		for _, w := range result.Data {
			fmt.Println(" ", w.Name)
		}

		// An empty cursor is the index saying the result set is exhausted. A short
		// page is not: a backend may return fewer hits than asked for and still have
		// more.
		if result.Cursor == "" {
			break
		}

		filter.Cursor = &result.Cursor
	}

	// Output:
	// page 1, resuming: false
	//   carrot
	//   carrot cake
	// page 2, resuming: true
	//   carrot soup
}

// Example_databaseFallback shows the price of the two cursor kinds sharing one
// field: a filter cursor is the last row's ID, an index cursor is an opaque
// token, and a caller dropping from the index to the database has to say which
// failures that fallback can cover for.
func Example_databaseFallback() {
	ctx := context.Background()

	cursor := "a-token-the-index-issued"
	filter := filtering.DefaultQueryFilter()
	filter.Cursor = &cursor

	// A backend that is merely down can be stood in for.
	unavailable := &exampleIndex{err: platformerrors.New("elasticsearch: connection refused")}

	_, err := searchpagination.Search(ctx, unavailable, "carrot", filter)
	fmt.Println("index failed:", err != nil)
	fmt.Println("cursor rejected:", searchpagination.CursorRejected(err))

	// The cursor cannot come along to the database, which would read the token as
	// an ID and match an arbitrary slice of the table rather than failing. Dropping
	// it restarts at the first page: results the client has already seen, but at
	// least the results they asked for. The caller's own filter is left alone,
	// since it still describes the search that was attempted.
	fallback := searchpagination.FilterForDatabaseFallback(filter)
	fmt.Println("fallback cursor dropped:", fallback.Cursor == nil)
	fmt.Println("original filter untouched:", *filter.Cursor)

	// A cursor the index refuses is the other answer. The database pages by a
	// different model and would serve the first page of its own pagination instead
	// of the page that was asked for, so this belongs in front of the client as its
	// own status — stop paging, or narrow the query.
	tooDeep := &exampleIndex{err: textsearch.ErrResultWindowExceeded}

	_, err = searchpagination.Search(ctx, tooDeep, "carrot", filter)
	fmt.Println("cursor rejected:", searchpagination.CursorRejected(err))

	// Output:
	// index failed: true
	// cursor rejected: false
	// fallback cursor dropped: true
	// original filter untouched: a-token-the-index-issued
	// cursor rejected: true
}

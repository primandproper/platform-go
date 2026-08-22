package fake

import (
	"github.com/primandproper/platform-go/v13/filtering"
)

// DefaultPageSize is how many elements BuildFakePage puts in a page.
//
// Three, because the properties a list test is usually after — that the elements are
// distinct, that they arrive in order, that a count matches — need more than one and
// are no better shown by a hundred.
const DefaultPageSize = 3

// BuildFakePage builds a page of fakes from the builder for one of them.
//
// A service that lists something has a test that needs a page of it, so every domain
// that lists anything grows one of these per type, and they are all the same function
// with the element type changed. What varies is the builder to call, which is what
// this takes.
func BuildFakePage[T any](build func() *T) *filtering.QueryFilteredResult[T] {
	return BuildFakePageOfSize(DefaultPageSize, build)
}

// BuildFakePageOfSize builds a page of the given size from the builder for one
// element, for the test whose subject is the size.
//
// The counts describe the page rather than a store behind it: a page of n elements
// reports n filtered and n total, which is what a caller reading the response would
// conclude. A test about a page that is a window onto something larger sets the counts
// it means.
//
// They are marked known, because a fake page is one that was answered. A test whose
// subject is the other case — a store that had no row to read its counts off — wants
// filtering.NewQueryFilteredResultWithoutCounts rather than this.
func BuildFakePageOfSize[T any](size int, build func() *T) *filtering.QueryFilteredResult[T] {
	data := make([]*T, 0, size)
	for range size {
		data = append(data, build())
	}

	return &filtering.QueryFilteredResult[T]{
		Data: data,
		Pagination: filtering.Pagination{
			Cursor:          BuildFakeID(),
			MaxResponseSize: filtering.DefaultQueryFilterLimit,
			FilteredCount:   uint64(size),
			TotalCount:      uint64(size),
			CountsKnown:     true,
		},
	}
}

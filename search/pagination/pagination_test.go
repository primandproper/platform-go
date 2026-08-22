package searchpagination

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
	textsearchmock "github.com/primandproper/platform-go/v13/search/text/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// exampleHit stands in for a search subset: what the index holds, as opposed to
// the domain object a client asked for.
type exampleHit struct {
	ID string
}

// exampleRow stands in for the domain object the store returns for a hit ID.
type exampleRow struct {
	ID   string
	Name string
}

func filterWithCursor(t *testing.T, cursor string) *filtering.QueryFilter {
	t.Helper()

	filter := filtering.DefaultQueryFilter()
	filter.Cursor = &cursor

	return filter
}

func TestSearch(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		const cursor = "cursor-from-a-previous-page"

		filter := filterWithCursor(t, cursor)
		filter.MaxResponseSize = new(uint16(17))

		expected := &textsearch.SearchResults[exampleHit]{Hits: []*exampleHit{{ID: "hit"}}}
		index := &textsearchmock.IndexMock[exampleHit]{
			SearchFunc: func(_ context.Context, req textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
				test.EqOp(t, "widgets", req.Query)
				test.EqOp(t, 17, req.Limit)
				test.EqOp(t, textsearch.Cursor(cursor), req.Cursor)

				return expected, nil
			},
		}

		actual, err := Search(ctx, index, "widgets", filter)
		must.NoError(t, err)
		test.Eq(t, expected, actual)
		test.SliceLen(t, 1, index.SearchCalls())
	})

	T.Run("with error searching", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		expected := platformerrors.New("blah")
		index := &textsearchmock.IndexMock[exampleHit]{
			SearchFunc: func(_ context.Context, _ textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
				return nil, expected
			},
		}

		actual, err := Search(ctx, index, "widgets", nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, expected)
	})

	T.Run("without an index", func(t *testing.T) {
		t.Parallel()

		actual, err := Search[exampleHit](t.Context(), nil, "widgets", nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

func TestRequestFromFilter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		const cursor = "cursor-from-a-previous-page"

		filter := filterWithCursor(t, cursor)
		filter.MaxResponseSize = new(uint16(17))

		actual := RequestFromFilter("widgets", filter)
		test.EqOp(t, "widgets", actual.Query)
		test.EqOp(t, 17, actual.Limit)
		test.EqOp(t, textsearch.Cursor(cursor), actual.Cursor)
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		actual := RequestFromFilter("widgets", nil)
		test.EqOp(t, "widgets", actual.Query)
		test.Zero(t, actual.Limit)
		test.True(t, actual.Cursor.IsZero())
	})

	T.Run("with a cursor pointing at the empty string", func(t *testing.T) {
		t.Parallel()

		// A client that sends cursor="" is asking for the first page, not resuming
		// from a token the index would have to make sense of.
		test.True(t, RequestFromFilter("widgets", filterWithCursor(t, "")).Cursor.IsZero())
	})
}

func TestResuming(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.True(t, Resuming(filterWithCursor(t, "cursor-from-a-previous-page")))
	})

	T.Run("without a cursor", func(t *testing.T) {
		t.Parallel()

		test.False(t, Resuming(filtering.DefaultQueryFilter()))
	})

	T.Run("with an empty cursor", func(t *testing.T) {
		t.Parallel()

		test.False(t, Resuming(filterWithCursor(t, "")))
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		test.False(t, Resuming(nil))
	})
}

func TestNewResult(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		const cursor = "cursor-from-a-previous-page"

		filter := filterWithCursor(t, cursor)
		data := []*exampleRow{{ID: "first"}, {ID: "second"}}

		actual := NewResult(data, textsearch.Cursor("cursor-for-the-next-page"), filter)
		test.Eq(t, data, actual.Data)
		test.EqOp(t, "cursor-for-the-next-page", actual.Cursor)
		test.EqOp(t, cursor, actual.PreviousCursor)
		test.EqOp(t, uint64(2), actual.FilteredCount)
		test.Eq(t, filter, actual.AppliedQueryFilter)
	})

	T.Run("carries the index's cursor rather than the last row's ID", func(t *testing.T) {
		t.Parallel()

		// This is the whole point of the type: paging onward means handing the index
		// back the token it issued, which has nothing to do with any row we fetched.
		data := []*exampleRow{{ID: "the-last-row"}}

		actual := NewResult(data, textsearch.Cursor("cursor-for-the-next-page"), filtering.DefaultQueryFilter())
		test.EqOp(t, "cursor-for-the-next-page", actual.Cursor)
	})

	T.Run("with the result set exhausted", func(t *testing.T) {
		t.Parallel()

		// An empty next cursor is how the index says there is no further page, and an
		// empty Pagination.Cursor is how we say it.
		actual := NewResult([]*exampleRow{{ID: "first"}}, "", filtering.DefaultQueryFilter())
		test.EqOp(t, "", actual.Cursor)
	})

	T.Run("reports an unknown total rather than the page size", func(t *testing.T) {
		t.Parallel()

		// Reporting len(data) as the total is what tells a client that a truncated
		// page was the entire result set.
		actual := NewResult([]*exampleRow{{ID: "first"}}, textsearch.Cursor("more"), filtering.DefaultQueryFilter())
		test.Zero(t, actual.TotalCount)
	})

	T.Run("vouches for neither count", func(t *testing.T) {
		t.Parallel()

		// Nothing counted anything: the index says whether there is more, not how
		// much. The fields keep the values a client already reads, and Counts is
		// what declines to present them as answers.
		actual := NewResult([]*exampleRow{{ID: "first"}}, textsearch.Cursor("more"), filtering.DefaultQueryFilter())

		test.False(t, actual.CountsKnown)
		test.EqOp(t, uint64(1), actual.FilteredCount)

		filtered, total, known := actual.Counts()

		test.False(t, known)
		test.Zero(t, filtered)
		test.Zero(t, total)
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		actual := NewResult([]*exampleRow{{ID: "first"}}, textsearch.Cursor("more"), nil)
		test.EqOp(t, "more", actual.Cursor)
		test.EqOp(t, "", actual.PreviousCursor)
	})
}

func TestFilterForDatabaseFallback(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		const cursor = "an-opaque-index-token"

		filter := filterWithCursor(t, cursor)

		actual := FilterForDatabaseFallback(filter)
		test.Nil(t, actual.Cursor)
		test.Eq(t, filter.MaxResponseSize, actual.MaxResponseSize)
		test.Eq(t, filter.SortBy, actual.SortBy)

		// The caller's filter is left alone, since it is still describing the search.
		must.NotNil(t, filter.Cursor)
		test.EqOp(t, cursor, *filter.Cursor)
	})

	T.Run("without a cursor", func(t *testing.T) {
		t.Parallel()

		filter := filtering.DefaultQueryFilter()
		test.EqOp(t, filter, FilterForDatabaseFallback(filter))
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, FilterForDatabaseFallback(nil))
	})
}

func TestCursorRejected(T *testing.T) {
	T.Parallel()

	T.Run("with a cursor the index did not issue", func(t *testing.T) {
		t.Parallel()

		test.True(t, CursorRejected(textsearch.ErrInvalidCursor))
	})

	T.Run("with pagination past the result window", func(t *testing.T) {
		t.Parallel()

		test.True(t, CursorRejected(textsearch.ErrResultWindowExceeded))
	})

	T.Run("with a wrapped error", func(t *testing.T) {
		t.Parallel()

		test.True(t, CursorRejected(platformerrors.Wrap(textsearch.ErrResultWindowExceeded, "searching")))
	})

	T.Run("with an unrelated error", func(t *testing.T) {
		t.Parallel()

		// A backend that is merely unreachable can be covered for by the database.
		test.False(t, CursorRejected(platformerrors.New("connection refused")))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		test.False(t, CursorRejected(nil))
	})
}

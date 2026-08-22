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

func hitID(hit *exampleHit) string {
	return hit.ID
}

func indexReturning(hits []*exampleHit, next textsearch.Cursor) *textsearchmock.IndexMock[exampleHit] {
	return &textsearchmock.IndexMock[exampleHit]{
		SearchFunc: func(_ context.Context, _ textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
			return &textsearch.SearchResults[exampleHit]{Hits: hits, NextCursor: next}, nil
		},
	}
}

func TestHydrated(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		const cursor = "cursor-from-a-previous-page"

		filter := filterWithCursor(t, cursor)
		filter.MaxResponseSize = new(uint16(17))

		index := &textsearchmock.IndexMock[exampleHit]{
			SearchFunc: func(_ context.Context, req textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
				// The filter's page size and resumption point reach the index, which is
				// the forgetting Search exists to prevent.
				test.EqOp(t, "widgets", req.Query)
				test.EqOp(t, 17, req.Limit)
				test.EqOp(t, textsearch.Cursor(cursor), req.Cursor)

				return &textsearch.SearchResults[exampleHit]{
					Hits:       []*exampleHit{{ID: "first"}, {ID: "second"}},
					NextCursor: textsearch.Cursor("cursor-for-the-next-page"),
				}, nil
			},
		}

		var hydratedWith []string

		rows := []*exampleRow{{ID: "first", Name: "carrot"}, {ID: "second", Name: "carrot cake"}}

		actual, err := Hydrated(ctx, index, "widgets", filter, hitID,
			func(_ context.Context, ids []string) ([]*exampleRow, error) {
				hydratedWith = ids

				return rows, nil
			},
		)
		must.NoError(t, err)

		test.Eq(t, []string{"first", "second"}, hydratedWith)
		test.Eq(t, rows, actual.Data)
		test.EqOp(t, "cursor-for-the-next-page", actual.Cursor)
		test.EqOp(t, cursor, actual.PreviousCursor)
		test.EqOp(t, uint64(2), actual.FilteredCount)

		// The two ways the loop gets written wrong: the page carries the index's
		// cursor rather than the last row's ID, and an unknown total rather than the
		// page size.
		test.NotEqOp(t, "second", actual.Cursor)
		test.Zero(t, actual.TotalCount)
	})

	T.Run("with nil filter", func(t *testing.T) {
		t.Parallel()

		index := indexReturning([]*exampleHit{{ID: "first"}}, "next")

		actual, err := Hydrated(t.Context(), index, "widgets", nil, hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return []*exampleRow{{ID: "first"}}, nil
			},
		)
		must.NoError(t, err)
		test.EqOp(t, "next", actual.Cursor)
		test.SliceLen(t, 1, actual.Data)
	})

	T.Run("with no hits", func(t *testing.T) {
		t.Parallel()

		// Asking a store to read zero IDs is a round trip for nothing at best, so the
		// hydrate never runs — but the index's cursor still reaches the caller.
		index := indexReturning([]*exampleHit{}, "next")

		hydrateCalls := 0

		actual, err := Hydrated(t.Context(), index, "widgets", filtering.DefaultQueryFilter(), hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				hydrateCalls++

				return nil, platformerrors.New("should not have been called")
			},
		)
		must.NoError(t, err)
		test.Zero(t, hydrateCalls)
		test.SliceEmpty(t, actual.Data)
		test.EqOp(t, "next", actual.Cursor)
		test.Zero(t, actual.FilteredCount)
	})

	T.Run("with an empty first page a caller can fall back on", func(t *testing.T) {
		t.Parallel()

		// An empty page part-way through a cursor walk is the end of the results; an
		// empty first page may be an index that is behind or was never populated.
		// Resuming is what tells those apart, and the caller decides.
		index := indexReturning(nil, "")

		filter := filtering.DefaultQueryFilter()

		actual, err := Hydrated(t.Context(), index, "widgets", filter, hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, nil
			},
		)
		must.NoError(t, err)
		test.SliceEmpty(t, actual.Data)
		test.False(t, Resuming(filter))
	})

	T.Run("with error searching", func(t *testing.T) {
		t.Parallel()

		// The index's refusal survives the trip out, so CursorRejected can still read
		// it and decline to fall back to the database.
		index := &textsearchmock.IndexMock[exampleHit]{
			SearchFunc: func(_ context.Context, _ textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
				return nil, textsearch.ErrInvalidCursor
			},
		}

		actual, err := Hydrated(t.Context(), index, "widgets", filtering.DefaultQueryFilter(), hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, platformerrors.New("should not have been called")
			},
		)
		test.Nil(t, actual)
		test.ErrorIs(t, err, textsearch.ErrInvalidCursor)
		test.True(t, CursorRejected(err))
	})

	T.Run("with error hydrating", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("blah")
		index := indexReturning([]*exampleHit{{ID: "first"}}, "next")

		actual, err := Hydrated(t.Context(), index, "widgets", filtering.DefaultQueryFilter(), hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, expected
			},
		)
		test.Nil(t, actual)
		test.ErrorIs(t, err, expected)
	})

	T.Run("with an index that reports neither results nor an error", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[exampleHit]{
			SearchFunc: func(_ context.Context, _ textsearch.SearchRequest) (*textsearch.SearchResults[exampleHit], error) {
				return nil, nil
			},
		}

		actual, err := Hydrated(t.Context(), index, "widgets", filtering.DefaultQueryFilter(), hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, platformerrors.New("should not have been called")
			},
		)
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrIndexReturnedNothing)
	})

	T.Run("without an index", func(t *testing.T) {
		t.Parallel()

		actual, err := Hydrated[exampleRow, exampleHit](t.Context(), nil, "widgets", nil, hitID,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, nil
			},
		)
		test.Nil(t, actual)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("without an ID extractor", func(t *testing.T) {
		t.Parallel()

		index := indexReturning([]*exampleHit{{ID: "first"}}, "next")

		actual, err := Hydrated[exampleRow](t.Context(), index, "widgets", nil, nil,
			func(_ context.Context, _ []string) ([]*exampleRow, error) {
				return nil, nil
			},
		)
		test.Nil(t, actual)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("without a hydrate function", func(t *testing.T) {
		t.Parallel()

		index := indexReturning([]*exampleHit{{ID: "first"}}, "next")

		actual, err := Hydrated[exampleRow](t.Context(), index, "widgets", nil, hitID, nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

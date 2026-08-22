package searchpagination

import (
	"context"
	"errors"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
)

// Search runs one page of query against the index, taking the page size and the
// resumption point from the caller's filter.
//
// It exists so that no call site can forget the limit: an unset limit is one
// page of the backend's choosing, and a caller that omits it gets search results
// truncated with no way to reach the rest.
func Search[T any](ctx context.Context, index textsearch.IndexSearcher[T], query string, filter *filtering.QueryFilter) (*textsearch.SearchResults[T], error) {
	if index == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "searching without an index")
	}

	return index.Search(ctx, RequestFromFilter(query, filter))
}

// RequestFromFilter builds the index request that a QueryFilter describes. A nil
// filter searches from the beginning at the index's default page size.
func RequestFromFilter(query string, filter *filtering.QueryFilter) textsearch.SearchRequest {
	req := textsearch.SearchRequest{Query: query}

	if filter == nil {
		return req
	}

	if filter.MaxResponseSize != nil {
		req.Limit = int(*filter.MaxResponseSize)
	}

	if Resuming(filter) {
		req.Cursor = textsearch.Cursor(*filter.Cursor)
	}

	return req
}

// Resuming reports whether filter carries a cursor, meaning the caller is
// part-way through a result set rather than asking for the first page.
//
// A cursor pointing at the empty string is a client asking for the first page,
// not a token the index has to make sense of, so it reads as not resuming.
func Resuming(filter *filtering.QueryFilter) bool {
	return filter != nil && filter.Cursor != nil && *filter.Cursor != ""
}

// NewResult wraps one page of hits, already resolved to domain objects, in the
// result type a list endpoint returns.
//
// data is what the store handed back for this page's hit IDs, and next is the
// cursor the index issued alongside those hits.
func NewResult[T any](data []*T, next textsearch.Cursor, filter *filtering.QueryFilter) *filtering.QueryFilteredResult[T] {
	// The total is left at zero, meaning unknown, as it is on the database search
	// path. The index reports whether another page exists but not how many results
	// there are in all, and reporting the page size as the total is what tells a
	// client that a truncated page was the entire result set.
	//
	// The ID extractor is a no-op because the cursor it would derive gets
	// overwritten below: paging onward from here means handing the index its own
	// token back, not the last row's ID.
	result := filtering.NewQueryFilteredResult(data, uint64(len(data)), 0, func(*T) string { return "" }, filter)

	// Nothing counted anything, so the pair is marked unknown. The fields keep the
	// values they have always carried, since a client reading them off the response
	// today should keep seeing what it saw; what changes is that
	// filtering.Pagination.Counts declines to vouch for them. That is the honest
	// report from an index that says whether there is more rather than how much —
	// the page size is how many rows came back, not how many matched it.
	result.CountsKnown = false

	// A zero cursor is how the index says the result set is exhausted and an empty
	// Pagination.Cursor is how we say it, so the two line up without translation.
	// Note that this is the end of the results, not a short page — a backend can
	// return fewer hits than asked for and still have more.
	result.Cursor = string(next)

	return result
}

// FilterForDatabaseFallback returns filter with any index cursor dropped, for a
// caller falling back to the database after searching the index.
//
// The cursor cannot come along. The database reads a cursor as the last row's ID
// and would compare an opaque token against the ID column, matching an arbitrary
// slice of the table rather than failing outright. Dropping it restarts at the
// first page, which repeats results the caller has already seen but is at least
// the results they asked for.
//
// The caller's own filter is left untouched, since it is still describing the
// search that was attempted.
func FilterForDatabaseFallback(filter *filtering.QueryFilter) *filtering.QueryFilter {
	if filter == nil || filter.Cursor == nil {
		return filter
	}

	clone := *filter
	clone.Cursor = nil

	return &clone
}

// CursorRejected reports whether err is the index declining the cursor it was
// given — either because it did not issue it, or because it will not page that
// deep.
//
// It marks the failures that a database fallback cannot cover for. A backend
// that is merely down can be stood in for; a rejected cursor cannot, because the
// database reads a cursor differently and would answer with the first page of
// its own pagination rather than the page that was asked for. Those belong in
// front of the client as their own status, so it can stop paging or narrow the
// query, rather than as a page from somewhere else.
//
// The two are matched separately rather than folded into one because they are
// not the same statement: an invalid cursor is one the index cannot read, and a
// depth limit is one it read fine and will not honor. What they share is the
// only thing this predicate asks about — the index declining the position it
// was handed, whichever backend is installed.
func CursorRejected(err error) bool {
	if err == nil {
		return false
	}

	return errors.Is(err, textsearch.ErrInvalidCursor) ||
		errors.Is(err, textsearch.ErrResultWindowExceeded)
}

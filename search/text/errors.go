package textsearch

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// The refusals a text index makes about the request rather than about itself.
//
// They live beside the interface, not in the backend that raises them, because
// they are answers a caller has to act on and a caller does not know which
// backend is installed. A service that matched elasticsearch.ErrEmptyQuery
// would stop matching the day it moved to Algolia — the refusal identical, the
// sentinel a different value — so the branch has to be written once per
// backend, in application code, for a distinction the interface says does not
// exist.
//
// The corollary is that a backend raises the one that describes its refusal,
// not one it invents. A backend with no result-window ceiling simply never
// returns ErrResultWindowExceeded.
//
// Both transports map all three: see errors/grpc and errors/http.
var (
	// ErrInvalidCursor is returned when a cursor cannot be decoded, or was issued
	// by a different backend than the one being asked to resume it.
	//
	// The second case is the common one in practice. A cursor is opaque, so it
	// travels in whatever field the API spells pagination with, and that field is
	// usually shared with the database-backed listings — which means a cursor
	// from a SQL page, or one left over from a backend swap, arrives here looking
	// exactly like a search cursor. Refusing it is the point: the alternative is
	// interpreting a position that happens to parse.
	ErrInvalidCursor = platformerrors.New("invalid search cursor")

	// ErrEmptyQueryProvided is returned when a search is attempted with no query
	// text. It is not a match-all, for the reason SearchRequest.Query gives: the
	// backends disagree about what an empty query means, and none of them means
	// "return the entire index".
	ErrEmptyQueryProvided = platformerrors.New("empty search query provided")

	// ErrResultWindowExceeded is returned when pagination reached the depth a
	// backend will serve — Elasticsearch's index.max_result_window, Algolia's
	// paginationLimitedTo. The cursor was valid and the page it named is past the
	// end of what can be paged to.
	//
	// It is an error rather than an empty last page because those are different
	// facts. An empty page says "that was everything", which a caller is entitled
	// to treat as authoritative; this says the result set continues and this
	// index will not walk to it, which a caller answers by narrowing the query,
	// not by paging again.
	ErrResultWindowExceeded = platformerrors.New("search result window exceeded")
)

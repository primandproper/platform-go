package searchpagination

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	textsearch "github.com/primandproper/platform-go/v13/search/text"
)

// ErrIndexReturnedNothing indicates a backend answered a search with neither
// results nor an error.
//
// It is an error rather than an empty final page so that a caller reading an
// exhausted result set can trust that the index said so, instead of being told
// there is no page two on the strength of a bug in the backend.
var ErrIndexReturnedNothing = platformerrors.New("search index returned no results and no error")

// Hydrated runs a text search end to end: one page of query against the index,
// then the store lookup that turns that page's hit IDs into domain objects,
// wrapped as the filtered result a list endpoint returns.
//
// T is what the index holds — a search subset, not the object a client asked
// for — and E is what the store hands back for those IDs. idOf reads the ID off
// a hit, and hydrate is the store's read-many, which is the only half of this
// that differs between call sites.
//
// Both type parameters are inferred from the arguments, so a call site spells
// neither:
//
//	page, err := searchpagination.Hydrated(ctx, s.widgetIndex, query, filter,
//		func(w *indexing.WidgetSubset) string { return w.ID },
//		s.db.GetWidgetsWithIDs,
//	)
//
// The resulting page carries the index's cursor, not the last row's ID, so a
// client paging onward hands the index back its own token. Its total is
// unknown, for the reason NewResult gives.
//
// An empty page of hits skips hydrate entirely and returns an empty result
// carrying whatever cursor the index issued. Asking a store to read zero IDs is
// at best a round trip for nothing and at worst a `WHERE id IN ()` that matches
// the whole table. A caller that treats an empty first page as reason to fall
// back to the database — because the index may be behind or was never populated
// — checks the returned page for that itself, with Resuming to tell an empty
// first page from the end of a cursor walk.
//
// The page is in the order hydrate returned, which is the store's order and not
// the index's relevance order unless the store preserves it. Where relevance
// order is what the client is being sold, hydrate is the place to restore it —
// it is the half of this that knows both the ID list it was given and the rows
// it read.
func Hydrated[E, T any](
	ctx context.Context,
	index textsearch.IndexSearcher[T],
	query string,
	filter *filtering.QueryFilter,
	idOf func(*T) string,
	hydrate func(ctx context.Context, ids []string) ([]*E, error),
) (*filtering.QueryFilteredResult[E], error) {
	if idOf == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "reading IDs from search hits")
	}

	if hydrate == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "hydrating search hits without a store lookup")
	}

	hits, err := Search(ctx, index, query, filter)
	if err != nil {
		// Wrapped rather than replaced: CursorRejected is how a caller decides
		// whether a database fallback can cover for this, and that answer has to
		// survive the trip out.
		return nil, platformerrors.Wrap(err, "searching index")
	}

	if hits == nil {
		return nil, platformerrors.Wrap(ErrIndexReturnedNothing, "searching index")
	}

	if len(hits.Hits) == 0 {
		return NewResult([]*E{}, hits.NextCursor, filter), nil
	}

	ids := make([]string, 0, len(hits.Hits))
	for _, hit := range hits.Hits {
		ids = append(ids, idOf(hit))
	}

	data, err := hydrate(ctx, ids)
	if err != nil {
		return nil, platformerrors.Wrap(err, "hydrating search hits")
	}

	return NewResult(data, hits.NextCursor, filter), nil
}

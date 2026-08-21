package dataprivacy

import (
	"context"
	"encoding/json"

	platformerrors "github.com/primandproper/platform-go/v12/errors"
	"github.com/primandproper/platform-go/v12/filtering"
)

// Fragment encodes a collector's result, or reports that the domain holds
// nothing about the subject.
//
// Returning nil, nil is how a Collector says "no data here", and the section is
// then omitted from the artifact rather than written as null. That distinction
// is the reason this helper is here rather than in each consumer: an artifact
// whose sections are the domains that actually held something reads as an
// answer, while one padded with empty objects for every domain in the
// application reads as a form. It is this package's contract, so this package
// states it once instead of every collector inferring it.
//
// held is passed rather than derived, because only the domain knows what
// holding something means for it. A slice is empty or it is not; a struct of
// counters is present whether or not any of them is non-zero, and a settings
// row that exists with every field at its default is data held about the
// subject.
func Fragment(held bool, v any) (json.RawMessage, error) {
	if !held {
		return nil, nil
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding dataprivacy fragment")
	}

	return encoded, nil
}

// CollectAll walks a cursor-paginated read to its end and returns every row.
//
// A collector that reads one page and stops returns a truncated subject access
// request, which is a compliance defect that looks exactly like a correct one —
// the artifact is well-formed, the section is present, and only the rows past
// the first page are missing. Paging is therefore not left to the caller to
// remember, and neither are filtering's cursor rules, which are easy to hold
// backwards:
//
//   - A page's Cursor is the last row's identifier, so it is empty only for a
//     page that held no rows. It is not a "there is more" signal — the final
//     full page carries one exactly as an intermediate page does.
//   - A page shorter than the size that was applied is the last one. The size
//     applied is the page's own, not the one requested, because a store that
//     clamps to something smaller would otherwise make its every page look
//     short and end the walk after the first.
//
// The walk asks for filtering.MaxQueryFilterLimit rows at a time, since nobody
// is reading these pages and each round trip is another chance for the read to
// fail halfway.
//
// Rows come back as values rather than pointers: the fragment a collector
// encodes is a document, and a slice of pointers marshals identically while
// inviting a nil element nobody checks for. A nil element is skipped here for
// the same reason. An empty result is nil rather than an empty slice, so it
// composes with Fragment's held flag without a length check at the call site.
//
// An error discards what was collected, per the Collector contract: the
// fragment is used or the error is recorded, and there is no path that returns
// a partial page count as though it were the domain's data. Cancellation is
// checked between pages, so an operation whose deadline expires mid-walk stops
// there rather than on the next page's error.
func CollectAll[T any](
	ctx context.Context,
	fetch func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error),
) ([]T, error) {
	if fetch == nil {
		return nil, ErrNilFetch
	}

	filter := filtering.DefaultQueryFilter()
	filter.MaxResponseSize = new(uint16(filtering.MaxQueryFilterLimit))

	var out []T

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		page, err := fetch(ctx, filter)
		if err != nil {
			return nil, err
		}

		if page == nil {
			return nil, ErrNilPage
		}

		for _, row := range page.Data {
			if row == nil {
				continue
			}

			out = append(out, *row)
		}

		// An empty cursor is an empty page, which is the end of the collection.
		if page.Cursor == "" {
			return out, nil
		}

		applied := *filter.MaxResponseSize
		if page.MaxResponseSize > 0 {
			applied = page.MaxResponseSize
		}

		if uint64(len(page.Data)) < uint64(applied) {
			return out, nil
		}

		// A full page that hands back the cursor it was given would be read
		// again forever. That is the store's bug, and it is reported rather
		// than quietly turned into a short export.
		if filter.Cursor != nil && page.Cursor == *filter.Cursor {
			return nil, platformerrors.Wrapf(ErrCursorStalled, "dataprivacy paged read repeated cursor %q", page.Cursor)
		}

		cursor := page.Cursor
		filter.SetCursor(&cursor)
	}
}

// CollectorFor builds a Collector from one paged read.
//
// This is what a collector is once the observability preamble is removed: page
// the domain's list read to its end, and encode the rows if there were any.
// Those two are this package's semantics rather than the domain's, and a
// consumer that implements them per domain implements them per domain
// correctly, eleven times, forever.
//
// What stays with the consumer is the span, the logger, and the repository
// call — the read is the domain's and always will be. A collector that wants
// the preamble writes it and calls CollectAll and Fragment itself; this
// constructor is for the common case where there is nothing else to say.
//
// held is len(rows) > 0, which is the right rule for a list read and the reason
// this covers a list read only. A domain whose "nothing held" is something else
// — a settings row that exists with every field defaulted, a counter struct
// that is present at zero — calls Fragment with its own answer.
//
// A nil fetch returns a nil Collector, which Registry.RegisterCollector
// refuses. That fails at wiring time rather than at the first export, which is
// where a domain silently missing from the artifact would otherwise be found.
func CollectorFor[T any](
	fetch func(ctx context.Context, subject Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error),
) Collector {
	if fetch == nil {
		return nil
	}

	return CollectorFunc(func(ctx context.Context, subject Subject) (json.RawMessage, error) {
		rows, err := CollectAll(ctx, func(ctx context.Context, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[T], error) {
			return fetch(ctx, subject, filter)
		})
		if err != nil {
			return nil, err
		}

		return Fragment(len(rows) > 0, rows)
	})
}

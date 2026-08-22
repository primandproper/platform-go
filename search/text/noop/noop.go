// Package noop is the textsearch.Index for a service with no search cluster:
// Index, Delete, and Wipe all succeed and keep nothing, and Search returns zero
// hits.
//
// Writes succeeding is what makes it usable — a handler that indexes a document
// after saving it needs no branch — and it is equally what makes it quiet, since
// a document that never reached Elasticsearch or Algolia looks exactly like one
// that did. The empty results carry no next cursor, so a caller paging through
// them terminates on the first call instead of looping over nothing.
//
// search/text/config builds it only for the "noop" provider name, which its
// validation requires rather than defaults to.
package noop

import (
	"context"

	textsearch "github.com/primandproper/platform-go/v13/search/text"
)

var _ textsearch.Index[any] = (*indexManager[any])(nil)

// indexManager is a noop Index.
type indexManager[T any] struct{}

// NewIndexManager returns a no-op Index.
func NewIndexManager[T any]() textsearch.Index[T] {
	return &indexManager[T]{}
}

// Search is a no-op method. It returns no hits and no next cursor, so a caller
// paging through results terminates on the first call rather than looping.
func (*indexManager[T]) Search(context.Context, textsearch.SearchRequest) (*textsearch.SearchResults[T], error) {
	return &textsearch.SearchResults[T]{Hits: []*T{}}, nil
}

// Index is a no-op method.
func (*indexManager[T]) Index(context.Context, string, any) error {
	return nil
}

// Delete is a no-op method.
func (*indexManager[T]) Delete(context.Context, string) error {
	return nil
}

// Wipe is a no-op method.
func (*indexManager[T]) Wipe(context.Context) error {
	return nil
}

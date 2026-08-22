// Package noop is the vectorsearch.Index for a deployment running no vector
// store: Upsert, Delete, and Wipe report success without keeping anything, and
// Query returns an empty result slice.
//
// Paired with the embeddings noop it makes a retrieval pipeline that runs end
// to end and retrieves nothing. That is the point when the feature is off, and
// a trap when it was meant to be on, because neither half errors and the empty
// result is shaped exactly like a query that legitimately matched nothing. A
// caller that needs to know whether semantic search is live has to ask its
// configuration; the results cannot tell it.
//
// search/vector/config builds it only for the "noop" provider name.
package noop

import (
	"context"

	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
)

var _ vectorsearch.Index[any] = (*indexManager[any])(nil)

// indexManager is a no-op vectorsearch.Index.
type indexManager[T any] struct{}

// NewIndex returns a no-op vectorsearch.Index that returns zero values for queries
// and silently succeeds on writes.
func NewIndex[T any]() vectorsearch.Index[T] {
	return &indexManager[T]{}
}

// Upsert is a no-op method.
func (*indexManager[T]) Upsert(context.Context, ...vectorsearch.Vector[T]) error {
	return nil
}

// Delete is a no-op method.
func (*indexManager[T]) Delete(context.Context, ...string) error {
	return nil
}

// Wipe is a no-op method.
func (*indexManager[T]) Wipe(context.Context) error {
	return nil
}

// Query is a no-op method that returns an empty result set.
func (*indexManager[T]) Query(context.Context, vectorsearch.QueryRequest) ([]vectorsearch.QueryResult[T], error) {
	return []vectorsearch.QueryResult[T]{}, nil
}

package noop

import (
	"context"

	vectorsearch "github.com/primandproper/platform-go/v3/search/vector"
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

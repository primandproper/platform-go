package noop

import (
	"context"

	textsearch "github.com/primandproper/platform-go/search/text"
)

var _ textsearch.Index[any] = (*indexManager[any])(nil)

// indexManager is a noop Index.
type indexManager[T any] struct{}

// NewIndexManager returns a no-op Index.
func NewIndexManager[T any]() textsearch.Index[T] {
	return &indexManager[T]{}
}

// Search is a no-op method.
func (*indexManager[T]) Search(context.Context, string) ([]*T, error) {
	return []*T{}, nil
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

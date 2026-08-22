// Package noop is the cache.Cache for a caller who wants no cache at all:
// every read misses and every write is accepted and forgotten.
//
// The consequence is that the cache is a permanent miss rather than a cold one,
// so whatever sits behind it must be able to serve every request from its
// source of truth, at full traffic, forever. Get returns cache.ErrNotFound and
// GetMany an empty map; the writes report success, because "stored" is a claim
// about a store the caller already knows is absent.
//
// SetIfPresent is the deliberate exception and returns cache.ErrNotFound, so a
// component whose correctness rests on a conditional write cannot be handed
// this cache and appear to work. See that method for the full argument.
//
// cache/config never builds this — a caller who wants it constructs it
// directly, which keeps "no cache" a decision written in code rather than a
// configuration fall-through.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/cache"
)

var _ cache.Cache[any] = (*Cache[any])(nil)

// Cache is a no-op Cache.
type Cache[T any] struct{}

// NewCache returns a no-op Cache. It returns the concrete type so a caller who
// deliberately wants no cache can say so in their own signatures, rather than
// naming the interface and leaving every reader to wonder which provider is
// behind it.
func NewCache[T any]() *Cache[T] {
	return &Cache[T]{}
}

// Get always returns ErrNotFound.
func (*Cache[T]) Get(context.Context, string) (*T, error) {
	return nil, cache.ErrNotFound
}

// Set is a no-op.
func (*Cache[T]) Set(context.Context, string, *T, ...cache.WriteOption) error {
	return nil
}

// SetIfPresent always returns ErrNotFound.
//
// It is the one write this cache refuses, and refusing is the point. Every
// other method here can no-op honestly, because "stored" and "deleted" are
// claims about a store the caller already knows is absent. A conditional write
// is different: answering nil would assert that the entry existed and was
// replaced, and a caller that reached for this method did so precisely because
// it needs that assertion to mean something.
//
// So a component whose correctness rests on a conditional write cannot be
// silently configured with a noop cache and appear to work. That is the same
// distinction cache.ErrUnavailable draws for an outage, applied to a store that
// is absent by configuration rather than by failure.
func (*Cache[T]) SetIfPresent(context.Context, string, *T, ...cache.WriteOption) error {
	return cache.ErrNotFound
}

// Delete is a no-op.
func (*Cache[T]) Delete(context.Context, string) error {
	return nil
}

// GetMany always returns an empty map.
func (*Cache[T]) GetMany(context.Context, []string) (map[string]*T, error) {
	return map[string]*T{}, nil
}

// SetMany is a no-op.
func (*Cache[T]) SetMany(context.Context, map[string]*T, ...cache.WriteOption) error {
	return nil
}

// DeleteMany is a no-op.
func (*Cache[T]) DeleteMany(context.Context, []string) error {
	return nil
}

// DeleteByPrefix is a no-op.
func (*Cache[T]) DeleteByPrefix(context.Context, string) error {
	return nil
}

// Flush is a no-op.
func (*Cache[T]) Flush(context.Context) error {
	return nil
}

// Ping is a no-op.
func (*Cache[T]) Ping(context.Context) error {
	return nil
}

// Close satisfies the interface and releases nothing.
func (c *Cache[T]) Close() error {
	return nil
}

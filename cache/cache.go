package cache

import (
	"context"

	"github.com/primandproper/platform-go/v3/errors"
)

var (
	ErrNotFound = errors.New("not found")
)

type (
	// Cache is our wrapper interface for a cache.
	Cache[T any] interface {
		Get(ctx context.Context, key string) (*T, error)
		Set(ctx context.Context, key string, value *T) error
		Delete(ctx context.Context, key string) error
		Ping(ctx context.Context) error
	}

	// BatchCache is a Cache that also supports batched reads and writes. Not
	// every Cache implementation supports batching, so callers should obtain a
	// BatchCache via a type assertion on a Cache value.
	BatchCache[T any] interface {
		Cache[T]
		// GetMany fetches multiple keys in as few round trips as possible.
		// Missing keys are omitted from the returned map, so a key's absence
		// from the result is a cache miss.
		GetMany(ctx context.Context, keys []string) (map[string]*T, error)
		// SetMany stores multiple values at once, each with the cache's
		// configured expiration.
		SetMany(ctx context.Context, items map[string]*T) error
	}
)

package cache_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
)

func ExampleCache_setAndGet() {
	ctx := context.Background()
	c, err := memory.NewInMemoryCache[string](0)
	if err != nil {
		panic(err)
	}

	value := "cached-value"
	if err = c.Set(ctx, "my-key", &value); err != nil {
		panic(err)
	}

	result, err := c.Get(ctx, "my-key")
	if err != nil {
		panic(err)
	}

	fmt.Println(*result)
	// Output: cached-value
}

func ExampleCache_batch() {
	ctx := context.Background()
	c, err := memory.NewInMemoryCache[string](0)
	if err != nil {
		panic(err)
	}

	// Batched reads and writes are part of Cache itself — no assertion needed.
	one, two := "one", "two"
	if err = c.SetMany(ctx, map[string]*string{"k1": &one, "k2": &two}); err != nil {
		panic(err)
	}

	// Missing keys are simply absent from the result.
	results, err := c.GetMany(ctx, []string{"k1", "k2", "missing"})
	if err != nil {
		panic(err)
	}

	fmt.Println(len(results))
	fmt.Println(*results["k1"])
	// Output:
	// 2
	// one
}

// ExampleNewDefaultCodec shows the codec the serializing providers use when
// given none. Consumers reach for Codec only to replace it — redis.WithCodec
// accepts any implementation whose Decode round-trips its own Encode. Note the
// migration caveat: values written under one codec are unreadable through
// another.
//
// It is also the codec to round-trip a type through before caching it, rather
// than a named one, so that a type which only a particular codec can encode is
// caught here rather than in a deployment.
func ExampleNewDefaultCodec() {
	type session struct {
		UserID string
		Roles  []string
	}

	codec := cache.NewDefaultCodec[session]()

	encoded, err := codec.Encode(&session{UserID: "u-1", Roles: []string{"admin", "auditor"}})
	if err != nil {
		panic(err)
	}

	decoded, err := codec.Decode(encoded)
	if err != nil {
		panic(err)
	}

	fmt.Println(decoded.UserID, decoded.Roles)
	// Output: u-1 [admin auditor]
}

func ExampleCache_notFound() {
	ctx := context.Background()
	c, cacheErr := memory.NewInMemoryCache[string](0)
	if cacheErr != nil {
		panic(cacheErr)
	}

	_, err := c.Get(ctx, "nonexistent")
	fmt.Println(err)
	fmt.Println(errors.Is(err, cache.ErrNotFound))
	// Output:
	// not found
	// true
}

// ExampleWithLoader memoizes an expensive computation. Concurrent readers that
// arrive while the computation is running share its result instead of each
// running their own, which is what keeps an expiring memo from turning every
// expiry into a stampede.
func ExampleWithLoader() {
	ctx := context.Background()

	var computations int

	c, err := memory.NewInMemoryCache[int](time.Minute,
		memory.WithLoader(func(_ context.Context, key string) (*int, error) {
			computations++
			total := len(key) * 100

			return &total, nil
		}),
		// Bounded, because a loader will answer for any key it is handed.
		memory.WithMaxEntries(1024, memory.EvictLeastRecentlyUsed),
	)
	if err != nil {
		panic(err)
	}

	for range 3 {
		total, getErr := c.Get(ctx, "east")
		if getErr != nil {
			panic(getErr)
		}

		fmt.Println(*total)
	}

	fmt.Println("computations:", computations)
	// Output:
	// 400
	// 400
	// 400
	// computations: 1
}

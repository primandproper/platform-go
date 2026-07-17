package cache_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/primandproper/platform-go/v5/cache"
	"github.com/primandproper/platform-go/v5/cache/memory"
)

func ExampleCache_setAndGet() {
	ctx := context.Background()
	c, err := memory.NewInMemoryCache[string](nil, nil, nil)
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

func ExampleBatchCache() {
	ctx := context.Background()
	c, err := memory.NewInMemoryCache[string](nil, nil, nil)
	if err != nil {
		panic(err)
	}

	// Not every Cache supports batching, so obtain a BatchCache by asserting.
	bc, ok := c.(cache.BatchCache[string])
	if !ok {
		panic("cache does not support batching")
	}

	one, two := "one", "two"
	if err = bc.SetMany(ctx, map[string]*string{"k1": &one, "k2": &two}); err != nil {
		panic(err)
	}

	// Missing keys are simply absent from the result.
	results, err := bc.GetMany(ctx, []string{"k1", "k2", "missing"})
	if err != nil {
		panic(err)
	}

	fmt.Println(len(results))
	fmt.Println(*results["k1"])
	// Output:
	// 2
	// one
}

func ExampleCache_notFound() {
	ctx := context.Background()
	c, cacheErr := memory.NewInMemoryCache[string](nil, nil, nil)
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

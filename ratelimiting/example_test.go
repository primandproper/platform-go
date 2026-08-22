package ratelimiting_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/ratelimiting"
)

func ExampleNewInMemoryRateLimiter() {
	limiter, err := ratelimiting.NewInMemoryRateLimiter(10.0, 5)
	if err != nil {
		panic(err)
	}

	// Closing is what stops the sweeper that reclaims the limiters of keys that
	// have stopped arriving.
	defer limiter.Close()

	var allowed bool
	allowed, err = limiter.Allow(context.Background(), "user-123")
	if err != nil {
		panic(err)
	}

	fmt.Println(allowed)
	// Output: true
}

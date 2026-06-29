package ratelimiting_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v2/ratelimiting"
)

func ExampleNewInMemoryRateLimiter() {
	limiter, err := ratelimiting.NewInMemoryRateLimiter(nil, 10.0, 5)
	if err != nil {
		panic(err)
	}

	var allowed bool
	allowed, err = limiter.Allow(context.Background(), "user-123")
	if err != nil {
		panic(err)
	}

	fmt.Println(allowed)
	// Output: true
}

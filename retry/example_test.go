package retry_test

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v3/retry"
)

func ExampleNewExponentialBackoffPolicy() {
	policy := retry.NewExponentialBackoffPolicy(retry.Config{
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Multiplier:   2.0,
	})

	attempts := 0
	err := policy.Execute(context.Background(), func(_ context.Context) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("not yet")
		}
		return nil
	})

	fmt.Println(err)
	fmt.Println(attempts)
	// Output:
	// <nil>
	// 3
}

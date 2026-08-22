package retrycfg_test

import (
	"context"
	"fmt"
	"time"

	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
)

func ExampleNewExponentialBackoffPolicy() {
	policy, err := retrycfg.NewExponentialBackoffPolicy(retrycfg.Config{
		MaxAttempts:  3,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
		Multiplier:   2.0,
	}, retrycfg.WithName("example"))
	if err != nil {
		panic(err)
	}

	attempts := 0
	err = policy.Execute(context.Background(), func(_ context.Context) error {
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

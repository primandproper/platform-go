package secrets_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/secrets"
	"github.com/primandproper/platform-go/v13/secrets/env"
)

func Example_envSecretSource() {
	os.Setenv("EXAMPLE_SECRET", "s3cret")
	defer os.Unsetenv("EXAMPLE_SECRET")

	source, err := env.NewSecretSource()
	if err != nil {
		panic(err)
	}
	defer source.Close()

	secret, err := source.GetSecret(context.Background(), "EXAMPLE_SECRET")
	if err != nil {
		panic(err)
	}

	fmt.Println(secret)
	// Output: s3cret
}

func Example_cachingSecretSource() {
	os.Setenv("EXAMPLE_CACHED_SECRET", "s3cret")
	defer os.Unsetenv("EXAMPLE_CACHED_SECRET")

	backend, err := env.NewSecretSource()
	if err != nil {
		panic(err)
	}

	// Five minutes of TTL with a refresh every minute: reads are answered from
	// memory, the round-trip happens on the refresh goroutine rather than in
	// anyone's request, and a rotation is picked up within a minute.
	source, err := secrets.NewCachingSource(backend, 5*time.Minute,
		secrets.WithRefresh(context.Background(), time.Minute))
	if err != nil {
		panic(err)
	}
	// Closing the cache closes the source it wraps.
	defer source.Close()

	for range 3 {
		secret, getErr := source.GetSecret(context.Background(), "EXAMPLE_CACHED_SECRET")
		if getErr != nil {
			panic(getErr)
		}

		fmt.Println(secret)
	}

	// Output:
	// s3cret
	// s3cret
	// s3cret
}

// rotatingSource stands in for a backend whose value is rotated out from under
// a running process.
type rotatingSource struct {
	reads atomic.Int64
}

func (r *rotatingSource) GetSecret(context.Context, string) (string, error) {
	if r.reads.Add(1) == 1 {
		return "old-signing-key", nil
	}

	return "new-signing-key", nil
}

func (r *rotatingSource) Close() error { return nil }

func Example_rotationHooks() {
	// A one-nanosecond TTL so the second read below re-reads immediately; a
	// real deployment measures this in minutes and lets WithRefresh do the
	// re-reading.
	source, err := secrets.NewCachingSource(&rotatingSource{}, time.Nanosecond)
	if err != nil {
		panic(err)
	}
	defer source.Close()

	rotated := make(chan string, 1)
	cancel := source.OnChange("signing-key", func(oldValue, newValue string) {
		rotated <- fmt.Sprintf("%s -> %s", oldValue, newValue)
	})
	defer cancel()

	// The first read has nothing to compare against, so no hook fires.
	if _, err = source.GetSecret(context.Background(), "signing-key"); err != nil {
		panic(err)
	}

	// The second sees a new value and reports it, which is the cue to rebuild
	// whatever was derived from the old one.
	if _, err = source.GetSecret(context.Background(), "signing-key"); err != nil {
		panic(err)
	}

	fmt.Println(<-rotated)

	// Output: old-signing-key -> new-signing-key
}

package idempotency_test

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/idempotency"
)

// charge is the recorded result: a concrete struct with exported fields, which
// is what the store can round-trip.
type charge struct {
	ID string
}

func newManager() (*idempotency.Manager[charge], error) {
	store, err := memory.NewInMemoryCache[idempotency.Record[charge]](0)
	if err != nil {
		return nil, err
	}

	locker, err := dlmemory.NewLocker()
	if err != nil {
		return nil, err
	}

	scoped, err := distributedlock.NewScopedLocker(locker)
	if err != nil {
		return nil, err
	}

	return idempotency.NewManager(store, scoped)
}

// ExampleManager_Do shows the shape the whole package exists for: the same key
// twice, the work once.
func ExampleManager_Do() {
	ctx := context.Background()

	manager, err := newManager()
	if err != nil {
		panic(err)
	}

	charges := 0
	authorize := func(context.Context) (*charge, error) {
		charges++

		return &charge{ID: "ch_1"}, nil
	}

	// The key and a fingerprint of the request the key is being used for. The
	// fingerprint is what stops one key from answering two different requests.
	const (
		key         = "d3f1a0c4-5b6e-4a2f-9c8d-1e2f3a4b5c6d"
		fingerprint = "sha256-of-the-request"
	)

	first, err := manager.Do(ctx, key, fingerprint, authorize)
	if err != nil {
		panic(err)
	}

	// The client never saw the response and retried with the same key.
	second, err := manager.Do(ctx, key, fingerprint, authorize)
	if err != nil {
		panic(err)
	}

	fmt.Println("first:", first.Value.ID, "replayed:", first.Replayed)
	fmt.Println("second:", second.Value.ID, "replayed:", second.Replayed)
	fmt.Println("charges:", charges)

	// Output:
	// first: ch_1 replayed: false
	// second: ch_1 replayed: true
	// charges: 1
}

// ExampleManager_Do_mismatch shows the same key used for a different request.
func ExampleManager_Do_mismatch() {
	ctx := context.Background()

	manager, err := newManager()
	if err != nil {
		panic(err)
	}

	authorize := func(context.Context) (*charge, error) { return &charge{ID: "ch_1"}, nil }

	const key = "d3f1a0c4-5b6e-4a2f-9c8d-1e2f3a4b5c6d"

	if _, err = manager.Do(ctx, key, "charge-10-dollars", authorize); err != nil {
		panic(err)
	}

	// Same key, different request. Replaying the first result would hide the
	// bug, so the reuse is reported instead.
	_, err = manager.Do(ctx, key, "charge-1000-dollars", authorize)

	fmt.Println(err)

	// Output:
	// matching idempotency fingerprint: idempotency key reused with a different request
}

// ExampleWithNewKey shows where a client mints its key: once, outside the retry
// loop, so every attempt sends the same one.
func ExampleWithNewKey() {
	ctx := context.Background()

	ctx, key := idempotency.WithNewKey(ctx)

	for attempt := range 3 {
		sent, _ := idempotency.KeyFromContext(ctx)
		fmt.Println("attempt", attempt, "sends the minted key:", sent == key)
	}

	// Output:
	// attempt 0 sends the minted key: true
	// attempt 1 sends the minted key: true
	// attempt 2 sends the minted key: true
}

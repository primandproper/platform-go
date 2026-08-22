package distributedlock_test

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/distributedlock/memory"
)

// ExampleNewScopedLocker wraps a plain Locker in the scoped surface: acquire,
// run, release — including on panic. Nothing is left for the caller to carry
// or to forget.
func ExampleNewScopedLocker() {
	ctx := context.Background()

	locker, err := memory.NewLocker()
	if err != nil {
		panic(err)
	}
	defer func() { _ = locker.Close() }()

	// nil logger, tracer provider, and metrics provider fall back to noop
	// implementations; a real service passes the ones it built at startup.
	scoped, err := distributedlock.NewScopedLocker(locker)
	if err != nil {
		panic(err)
	}

	if err = scoped.WithLock(ctx, "nightly-compaction", func(context.Context) error {
		fmt.Println("compacting")

		return nil
	}); err != nil {
		panic(err)
	}

	// The lock was released the moment fn returned, so the next caller gets it
	// without waiting.
	ran, err := scoped.TryWithLock(ctx, "nightly-compaction", func(context.Context) error {
		fmt.Println("compacting again")

		return nil
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("ran:", ran)
	// Output:
	// compacting
	// compacting again
	// ran: true
}

// ExampleScopedLocker_TryWithLock demonstrates the return worth reading
// carefully: a contended lock yields (false, nil). fn did not run, and nothing
// went wrong — losing the election is the expected outcome for every replica
// but one, not an error to report.
func ExampleScopedLocker_TryWithLock() {
	ctx := context.Background()

	locker, err := memory.NewLocker()
	if err != nil {
		panic(err)
	}
	defer func() { _ = locker.Close() }()

	// nil logger, tracer provider, and metrics provider fall back to noop
	// implementations; a real service passes the ones it built at startup.
	scoped, err := distributedlock.NewScopedLocker(locker)
	if err != nil {
		panic(err)
	}

	// Stand in for another replica that currently holds the lock.
	held, err := locker.Acquire(ctx, "janitor", time.Minute)
	if err != nil {
		panic(err)
	}

	ran, err := scoped.TryWithLock(ctx, "janitor", func(context.Context) error {
		fmt.Println("this never runs")

		return nil
	})
	fmt.Println("ran:", ran, "err:", err)

	if err = held.Release(ctx); err != nil {
		panic(err)
	}

	ran, err = scoped.TryWithLock(ctx, "janitor", func(context.Context) error {
		fmt.Println("sweeping")

		return nil
	})
	if err != nil {
		panic(err)
	}

	fmt.Println("ran:", ran)
	// Output:
	// ran: false err: <nil>
	// sweeping
	// ran: true
}

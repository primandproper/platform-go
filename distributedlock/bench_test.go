package distributedlock_test

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/distributedlock/memory"

	"github.com/shoenig/test/must"
)

// BenchmarkScopedLocker measures what the generic adapter adds on top of the
// Locker it wraps. Backing it with the in-memory locker makes these rows
// directly comparable to distributedlock/memory's Locker_AcquireRelease: the
// difference is the adapter's deferred release and error join, not lock
// infrastructure.
//
// Every case here is uncontended on the acquire path, so WithLock never
// reaches its polling loop — the poll interval is a wait for another holder to
// finish, not a cost the adapter imposes.
func BenchmarkScopedLocker(b *testing.B) {
	raw, err := memory.NewLocker()
	must.NoError(b, err)

	scoped, err := distributedlock.NewScopedLocker(raw, distributedlock.WithScopedLockTTL(time.Minute))
	must.NoError(b, err)

	ctx := b.Context()
	noop := func(context.Context) error { return nil }

	b.Run("WithLock", func(b *testing.B) {
		for b.Loop() {
			_ = scoped.WithLock(ctx, "bench-key", noop)
		}
	})

	b.Run("TryWithLock/free", func(b *testing.B) {
		for b.Loop() {
			_, _ = scoped.TryWithLock(ctx, "bench-key", noop)
		}
	})

	b.Run("TryWithLock/held", func(b *testing.B) {
		// The rejection path, which is what most replicas take: a singleton
		// chore is claimed by one process and every other one bounces off this
		// call for the life of the deployment.
		held, acquireErr := raw.Acquire(ctx, "bench-held-key", time.Hour)
		must.NoError(b, acquireErr)
		b.Cleanup(func() { _ = held.Release(context.WithoutCancel(ctx)) })

		for b.Loop() {
			_, _ = scoped.TryWithLock(ctx, "bench-held-key", noop)
		}
	})
}

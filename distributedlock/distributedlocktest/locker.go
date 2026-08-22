package distributedlocktest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/identifiers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

const (
	// heldTTL is the TTL for every case that is not about expiry. It is long
	// enough that no backend, however loaded, can expire a lock in the middle
	// of a case that assumes it still holds one.
	heldTTL = time.Minute

	// expiryTTL is the TTL the expiry cases acquire with, and expiryWait is how
	// long they then wait before asserting the lock has lapsed. The gap between
	// them is the whole tolerance the suite has for a slow host, so it is wide:
	// waiting three times the window costs a few seconds of wall clock in
	// subtests that run in parallel anyway, and buys a case that does not flake.
	expiryTTL  = time.Second
	expiryWait = 3 * time.Second

	// contenders is how many goroutines race for one key in the case that
	// proves exactly one of them wins.
	contenders = 8
)

// Factory builds one Locker for one subtest. It must hand back a fresh
// instance and register whatever teardown that instance needs on tb — the
// suite never closes what a factory returns, because what Close does is one of
// the things this package deliberately leaves to each implementation.
//
// A backend whose store outlives the Locker value (a redis server, a postgres
// cluster) needs no cleaning between subtests: every key the suite touches
// carries a unique suffix, so one server can serve every subtest, every
// parallel run, and every rerun without collisions.
type Factory func(tb testing.TB) distributedlock.Locker

// Option declares where an implementation stops honoring the full Locker
// contract. Each one removes cases, so an implementation that declares nothing
// is held to all of it.
type Option func(*deviations)

// deviations is the set of declared departures from the full contract.
type deviations struct {
	advisoryTTL        bool
	instanceLocalStore bool
}

// WithAdvisoryTTL declares that this Locker's TTL is its own bookkeeping
// rather than something the store enforces: the holder stops owning the lock
// when the TTL lapses, but the key is not freed for anybody else until that
// holder releases it or its session dies.
//
// The postgres provider is this one — advisory locks have no server-side
// expiry — and it is why the case where a second caller takes over a lapsed
// key does not run for it. Everything else about expiry still does: a holder
// past its TTL is told ErrLockNotHeld by Release and by Refresh, which is the
// half of the promise a client-side expiry can keep.
func WithAdvisoryTTL() Option {
	return func(d *deviations) {
		d.advisoryTTL = true
	}
}

// WithInstanceLocalStore declares that this Locker keeps its locks inside the
// Locker value rather than somewhere a second holder could reach them, so
// mutual exclusion holds only among callers sharing one instance.
//
// The memory provider is this one. It is a deployment constraint rather than a
// testing detail — a second replica does not contend with the first, and finds
// every key free — so declaring it here is the implementation saying out loud
// what its doc already says in prose.
func WithInstanceLocalStore() Option {
	return func(d *deviations) {
		d.instanceLocalStore = true
	}
}

// Run asserts every behavior a distributedlock.Locker owes its callers against
// the implementation newLocker builds, as one parallel subtest per behavior.
//
// It takes a *testing.T rather than the testing.TB the factory takes because
// it runs subtests, which TB cannot: a failure has to name the behavior that
// broke, not just the provider.
func Run(t *testing.T, newLocker Factory, opts ...Option) {
	t.Helper()

	var d deviations
	for _, opt := range opts {
		if opt != nil {
			opt(&d)
		}
	}

	t.Run("Acquire hands back a handle echoing the key and TTL", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("echo")

		held, err := locker.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		must.NotNil(t, held)
		test.EqOp(t, key, held.Key())
		test.EqOp(t, heldTTL, held.TTL())
		must.NoError(t, held.Release(ctx))
	})

	t.Run("Acquire rejects an empty key", func(t *testing.T) {
		t.Parallel()

		locker := newLocker(t)

		held, err := locker.Acquire(t.Context(), "", heldTTL)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
		test.Nil(t, held)
	})

	t.Run("Acquire rejects a non-positive TTL", func(t *testing.T) {
		t.Parallel()

		locker := newLocker(t)

		// Zero and negative are one rule, and a provider that checks `ttl == 0`
		// instead of `ttl <= 0` hands back a handle that is already expired.
		for _, ttl := range []time.Duration{0, -time.Second} {
			held, err := locker.Acquire(t.Context(), uniqueKey("invalid_ttl"), ttl)
			must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
			test.Nil(t, held)
		}
	})

	t.Run("Refresh rejects a non-positive TTL", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		held, err := locker.Acquire(ctx, uniqueKey("refresh_invalid_ttl"), heldTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, held) })

		// The lock is still held: this is the argument being refused, not the
		// ownership.
		must.ErrorIs(t, held.Refresh(ctx, 0), distributedlock.ErrInvalidTTL)
		must.ErrorIs(t, held.Refresh(ctx, -time.Second), distributedlock.ErrInvalidTTL)
		test.EqOp(t, heldTTL, held.TTL())
	})

	t.Run("a held key is not acquirable again", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("contended")

		held, err := locker.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, held) })

		second, err := locker.Acquire(ctx, key, heldTTL)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		test.Nil(t, second)
	})

	t.Run("distinct keys do not contend", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		first, err := locker.Acquire(ctx, uniqueKey("independent_a"), heldTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, first) })

		second, err := locker.Acquire(ctx, uniqueKey("independent_b"), heldTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, second) })
	})

	t.Run("a released key is acquirable again", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("reacquire")

		first, err := locker.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		must.NoError(t, first.Release(ctx))

		second, err := locker.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		must.NoError(t, second.Release(ctx))
	})

	t.Run("a crowd racing for one key produces exactly one winner", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("race")

		var (
			wg      sync.WaitGroup
			winners atomic.Int64
			mu      sync.Mutex
			won     []distributedlock.Lock
		)

		wg.Add(contenders)
		for range contenders {
			go func() {
				defer wg.Done()

				held, err := locker.Acquire(ctx, key, heldTTL)
				if err != nil {
					// Everyone who lost has to have lost for the one documented
					// reason. A provider that reports contention as a transport
					// error makes every caller's retry loop wrong.
					test.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)

					return
				}

				winners.Add(1)

				mu.Lock()
				won = append(won, held)
				mu.Unlock()
			}()
		}
		wg.Wait()

		test.EqOp(t, int64(1), winners.Load())
		for _, held := range won {
			must.NoError(t, held.Release(ctx))
		}
	})

	t.Run("a lock taken through one Locker is visible to another", func(t *testing.T) {
		t.Parallel()

		if d.instanceLocalStore {
			t.Skip("implementation declared WithInstanceLocalStore: its locks live in the Locker value, so a second instance cannot see them")
		}

		ctx := t.Context()
		first, second := newLocker(t), newLocker(t)
		key := uniqueKey("cross_instance")

		held, err := first.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, held) })

		_, err = second.Acquire(ctx, key, heldTTL)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	t.Run("releasing twice reports the lock as no longer held", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		held, err := locker.Acquire(ctx, uniqueKey("double_release"), heldTTL)
		must.NoError(t, err)
		must.NoError(t, held.Release(ctx))
		must.ErrorIs(t, held.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	t.Run("refreshing after release reports the lock as no longer held", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		held, err := locker.Acquire(ctx, uniqueKey("refresh_after_release"), heldTTL)
		must.NoError(t, err)
		must.NoError(t, held.Release(ctx))
		must.ErrorIs(t, held.Refresh(ctx, heldTTL), distributedlock.ErrLockNotHeld)
	})

	t.Run("Refresh extends the lock past its original TTL", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("refresh_extends")

		held, err := locker.Acquire(ctx, key, expiryTTL)
		must.NoError(t, err)
		must.NoError(t, held.Refresh(ctx, heldTTL))
		test.EqOp(t, heldTTL, held.TTL())

		time.Sleep(expiryWait)

		// Past the TTL it was acquired with, and still held: nobody else can
		// take the key, and the holder can still release it.
		_, err = locker.Acquire(ctx, key, heldTTL)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		must.NoError(t, held.Release(ctx))
	})

	t.Run("releasing after the TTL lapses reports the lock as no longer held", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		held, err := locker.Acquire(ctx, uniqueKey("release_after_expiry"), expiryTTL)
		must.NoError(t, err)

		time.Sleep(expiryWait)

		must.ErrorIs(t, held.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	t.Run("refreshing after the TTL lapses reports the lock as no longer held", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		locker := newLocker(t)

		held, err := locker.Acquire(ctx, uniqueKey("refresh_after_expiry"), expiryTTL)
		must.NoError(t, err)
		t.Cleanup(func() { releaseQuietly(t, held) })

		time.Sleep(expiryWait)

		// A lapsed holder must be told to reacquire rather than left believing
		// a Refresh renewed something it no longer owns.
		must.ErrorIs(t, held.Refresh(ctx, heldTTL), distributedlock.ErrLockNotHeld)
	})

	t.Run("a lapsed lock is acquirable by the next caller", func(t *testing.T) {
		t.Parallel()

		if d.advisoryTTL {
			t.Skip("implementation declared WithAdvisoryTTL: the TTL is client-side bookkeeping, so a lapsed key stays taken until its holder releases it")
		}

		ctx := t.Context()
		locker := newLocker(t)
		key := uniqueKey("expired_key_frees")

		_, err := locker.Acquire(ctx, key, expiryTTL)
		must.NoError(t, err)

		time.Sleep(expiryWait)

		// Deliberately without releasing the first handle: expiry is what frees
		// the key here, which is what makes the TTL a bound on a crashed
		// holder rather than a suggestion.
		second, err := locker.Acquire(ctx, key, heldTTL)
		must.NoError(t, err)
		must.NoError(t, second.Release(ctx))
	})

	t.Run("Ping reaches the backend", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, newLocker(t).Ping(t.Context()))
	})
}

// releaseQuietly releases a handle a case is done with, tolerating the one
// error a cleanup can legitimately see: a case that already released the lock,
// or let it lapse, is not failed for it. Anything else is a real defect and
// fails the test.
func releaseQuietly(tb testing.TB, held distributedlock.Lock) {
	tb.Helper()

	// The test's own context is canceled just before cleanups run, and a
	// release on a canceled context fails at the transport rather than at the
	// lock — which would say nothing about whether the lock was held.
	if err := held.Release(context.WithoutCancel(tb.Context())); err != nil {
		test.ErrorIs(tb, err, distributedlock.ErrLockNotHeld)
	}
}

// uniqueKey suffixes a name so that subtests sharing one backend — a redis
// server, a postgres cluster — cannot collide with each other, and a rerun
// cannot inherit a key an earlier run left behind.
func uniqueKey(name string) string {
	return "distributedlocktest_" + name + "_" + identifiers.New()
}

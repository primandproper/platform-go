package distributedlocktest

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// waitForRelease bounds how long a case will wait for a contended WithLock to
// be granted once the holder returns. It is a failure bound, not a timing
// assertion: the natively-waiting implementation is granted the lock in
// microseconds and the polling adapter within its poll interval, so anything
// approaching this means the wait never ended.
const waitForRelease = 30 * time.Second

// settleForContention is how long a case waits before concluding that a
// contended WithLock has not run fn. Nothing guarantees it would have by then,
// so this direction of the assertion is one-sided on purpose: it can catch an
// implementation that runs fn without the lock, and it cannot fail one that is
// merely slow to acquire.
const settleForContention = 250 * time.Millisecond

// errFromFn is the error the cases that pass an error out of fn use. It is a
// sentinel of this package's own so that an assertion cannot mistake it for
// something the implementation raised on its own behalf.
var errFromFn = platformerrors.New("error returned by the fn under test")

// ScopedFactory builds one ScopedLocker for one subtest. As with Factory it
// hands back a fresh instance and registers whatever teardown that instance
// needs on tb.
//
// Every case here contends one instance against itself, so a factory over an
// instance-local Locker needs no deviation declared: there is nothing this
// interface promises about two of them that the scoped surface can be asked
// for. Where that promise matters it belongs to the Locker underneath, and
// Run is where it is asserted.
type ScopedFactory func(tb testing.TB) distributedlock.ScopedLocker

// RunScoped asserts every behavior a distributedlock.ScopedLocker owes its
// callers against the implementation newScopedLocker builds.
//
// The interface has two implementations that reach the same contract by
// opposite means — postgres waits in the database on a transaction-scoped
// advisory lock, the generic adapter polls Acquire — so the answers that
// matter here are the ones a caller writes code against and cannot see the
// mechanism behind: whether a contended TryWithLock is an error or a false,
// whether fn ran at all when it was, and whether the lock is free again after
// fn panics.
func RunScoped(t *testing.T, newScopedLocker ScopedFactory) {
	t.Helper()

	t.Run("WithLock runs fn and returns nil", func(t *testing.T) {
		t.Parallel()

		scoped := newScopedLocker(t)

		var ran bool
		must.NoError(t, scoped.WithLock(t.Context(), uniqueKey("scoped_happy"), func(context.Context) error {
			ran = true

			return nil
		}))
		test.True(t, ran)
	})

	t.Run("WithLock returns fn's error unchanged", func(t *testing.T) {
		t.Parallel()

		scoped := newScopedLocker(t)

		err := scoped.WithLock(t.Context(), uniqueKey("scoped_fn_error"), func(context.Context) error {
			return errFromFn
		})
		must.ErrorIs(t, err, errFromFn)
	})

	t.Run("WithLock rejects an empty key", func(t *testing.T) {
		t.Parallel()

		scoped := newScopedLocker(t)

		var ran bool
		err := scoped.WithLock(t.Context(), "", func(context.Context) error {
			ran = true

			return nil
		})
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
		test.False(t, ran)
	})

	t.Run("WithLock releases the lock when fn returns", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("scoped_releases")

		must.NoError(t, scoped.WithLock(ctx, key, func(context.Context) error { return nil }))

		// Try rather than WithLock: a lock that was not released would make
		// this hang rather than report, and the report is the point.
		acquired, err := scoped.TryWithLock(ctx, key, func(context.Context) error { return nil })
		must.NoError(t, err)
		test.True(t, acquired)
	})

	t.Run("WithLock releases the lock when fn returns an error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("scoped_releases_on_error")

		must.ErrorIs(t, scoped.WithLock(ctx, key, func(context.Context) error { return errFromFn }), errFromFn)

		acquired, err := scoped.TryWithLock(ctx, key, func(context.Context) error { return nil })
		must.NoError(t, err)
		test.True(t, acquired)
	})

	// Both entry points, because they are separate paths in every
	// implementation: postgres opens its own transaction per method, and the
	// adapter reaches its release through a different branch for each.
	t.Run("a panic inside WithLock reaches the caller and still releases the lock", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("with_panic")

		assertPanicReleases(t, scoped, key, func(fn func(context.Context) error) error {
			return scoped.WithLock(ctx, key, fn)
		})
	})

	t.Run("a panic inside TryWithLock reaches the caller and still releases the lock", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("try_panic")

		assertPanicReleases(t, scoped, key, func(fn func(context.Context) error) error {
			_, err := scoped.TryWithLock(ctx, key, fn)

			return err
		})
	})

	t.Run("TryWithLock runs fn and reports the lock as acquired", func(t *testing.T) {
		t.Parallel()

		scoped := newScopedLocker(t)

		var ran bool
		acquired, err := scoped.TryWithLock(t.Context(), uniqueKey("try_happy"), func(context.Context) error {
			ran = true

			return nil
		})
		must.NoError(t, err)
		test.True(t, acquired)
		test.True(t, ran)
	})

	t.Run("TryWithLock reports fn's error with the lock still reported as acquired", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("try_fn_error")

		// The two return values answer different questions — did I get the
		// lock, and how did my work go — and folding fn's failure into a false
		// would tell the caller to retry work that already ran.
		acquired, err := scoped.TryWithLock(ctx, key, func(context.Context) error {
			return errFromFn
		})
		must.ErrorIs(t, err, errFromFn)
		test.True(t, acquired)

		// And the lock went back on fn's error path as it does on its happy
		// one: a failing fn that stranded the lock would wedge every later
		// caller of that key.
		acquired, err = scoped.TryWithLock(ctx, key, func(context.Context) error { return nil })
		must.NoError(t, err)
		test.True(t, acquired)
	})

	t.Run("TryWithLock reports contention as false rather than an error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("try_contended")

		holder := holdKey(t, scoped, key)

		var ran atomic.Bool
		acquired, err := scoped.TryWithLock(ctx, key, func(context.Context) error {
			ran.Store(true)

			return nil
		})

		// Contention is the expected outcome of a Try, not a failure: a caller
		// that has to distinguish "someone else has it" from "the store is
		// down" by inspecting an error will get it wrong.
		must.NoError(t, err)
		test.False(t, acquired)
		test.False(t, ran.Load())

		holder.stop(t)
	})

	t.Run("WithLock waits for a contended lock rather than failing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped := newScopedLocker(t)
		key := uniqueKey("with_contended")

		holder := holdKey(t, scoped, key)

		var ran atomic.Bool
		waited := make(chan error, 1)
		go func() {
			waited <- scoped.WithLock(ctx, key, func(context.Context) error {
				ran.Store(true)

				return nil
			})
		}()

		time.Sleep(settleForContention)
		test.False(t, ran.Load(), test.Sprint("fn ran while another caller held the lock"))

		holder.stop(t)

		select {
		case err := <-waited:
			must.NoError(t, err)
			test.True(t, ran.Load())
		case <-time.After(waitForRelease):
			t.Fatalf("a contended WithLock did not run fn within %s of the holder returning", waitForRelease)
		}
	})
}

// assertPanicReleases panics inside fn and asserts both halves of what the
// interface promises about it: the panic reaches the caller, and the lock is
// free afterward.
//
// The second half is the consequential one. A scoped lock whose release is
// skipped by a panicking fn is held until its TTL lapses — or forever, where
// the implementation has no TTL — and the goroutine that would have released
// it is the one that just unwound through the frame.
func assertPanicReleases(t *testing.T, scoped distributedlock.ScopedLocker, key string, call func(fn func(context.Context) error) error) {
	t.Helper()

	recovered := func() (r any) {
		defer func() { r = recover() }()

		// This cannot return: fn panics, and the contract is that the panic
		// passes through to the caller once the lock has been released.
		return call(func(context.Context) error { panic(errFromFn) })
	}()
	must.NotNil(t, recovered)

	acquired, err := scoped.TryWithLock(t.Context(), key, func(context.Context) error { return nil })
	must.NoError(t, err)
	test.True(t, acquired, test.Sprint("the lock was still held after fn panicked"))
}

// heldKey is an in-flight WithLock parked inside fn, holding its key until
// stop is called. It is how the contention cases arrange for a lock to be
// genuinely held by somebody else for the duration of an assertion.
type heldKey struct {
	release chan struct{}
	done    chan error
}

// holdKey starts a WithLock that has entered fn and will stay there. It
// returns once the lock is actually held, so a case that acquires afterward is
// contending rather than racing.
func holdKey(tb testing.TB, scoped distributedlock.ScopedLocker, key string) *heldKey {
	tb.Helper()

	h := &heldKey{
		release: make(chan struct{}),
		done:    make(chan error, 1),
	}

	entered := make(chan struct{})
	go func() {
		h.done <- scoped.WithLock(context.WithoutCancel(tb.Context()), key, func(context.Context) error {
			close(entered)
			<-h.release

			return nil
		})
	}()

	select {
	case <-entered:
	case err := <-h.done:
		must.NoError(tb, err)
		tb.Fatalf("the holder of %q returned without entering fn", key)
	case <-time.After(waitForRelease):
		tb.Fatalf("could not take the lock on %q within %s", key, waitForRelease)
	}

	return h
}

// stop lets the held key go and waits for its WithLock to return.
func (h *heldKey) stop(tb testing.TB) {
	tb.Helper()

	close(h.release)

	select {
	case err := <-h.done:
		must.NoError(tb, err)
	case <-time.After(waitForRelease):
		tb.Fatalf("the holder's WithLock did not return within %s of fn finishing", waitForRelease)
	}
}

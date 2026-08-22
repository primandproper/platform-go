package memory

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/distributedlock/distributedlocktest"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func newTestLocker(t *testing.T) distributedlock.Locker {
	t.Helper()
	l, err := NewLocker()
	must.NoError(t, err)
	must.NotNil(t, l)
	return l
}

// newRecordingLocker builds a Locker with a RecordingObserver swapped in, so a
// test can both drive the Locker and assert which fields it observed.
func newRecordingLocker(t *testing.T) (*Locker, *observability.RecordingObserver) {
	t.Helper()
	l, err := NewLocker()
	must.NoError(t, err)
	must.NotNil(t, l)

	obs := observability.NewRecordingObserver()
	l.o11y = obs

	return l, obs
}

func TestNewLocker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker()
		must.NoError(t, err)
		test.NotNil(t, l)
	})
}

func TestLocker_Acquire(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		l, obs := newRecordingLocker(t)
		lock, err := l.Acquire(t.Context(), "k", time.Second)
		must.NoError(t, err)
		must.NotNil(t, lock)
		test.EqOp(t, "k", lock.Key())
		test.EqOp(t, time.Second, lock.TTL())

		obs.ObservedOperationWithData(t, map[string]any{
			"lock.key": "k",
			"lock.ttl": time.Second,
		})
	})

	T.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		l, obs := newRecordingLocker(t)
		_, err := l.Acquire(t.Context(), "", time.Second)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)

		// Even on the rejection path, the inputs are still observed.
		obs.ObservedOperationWithData(t, map[string]any{
			"lock.key": "",
			"lock.ttl": time.Second,
		})
	})

	T.Run("rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		l, obs := newRecordingLocker(t)
		_, err := l.Acquire(t.Context(), "k", time.Duration(0))
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)

		obs.ObservedOperationWithData(t, map[string]any{
			"lock.key": "k",
			"lock.ttl": time.Duration(0),
		})
	})

	T.Run("sweeps expired entries for other keys", func(t *testing.T) {
		t.Parallel()
		l, _ := newRecordingLocker(t)

		// Acquire a short-lived lock on keyA and let its TTL elapse.
		_, err := l.Acquire(t.Context(), "keyA", time.Millisecond)
		must.NoError(t, err)
		time.Sleep(10 * time.Millisecond)

		// Acquiring an unrelated key must sweep keyA's now-expired entry rather than
		// leaving it to accumulate for the life of the process.
		_, err = l.Acquire(t.Context(), "keyB", time.Minute)
		must.NoError(t, err)

		l.mu.Lock()
		_, stillPresent := l.held["keyA"]
		mapLen := len(l.held)
		l.mu.Unlock()

		test.False(t, stillPresent)
		test.EqOp(t, 1, mapLen)
	})
}

func TestLocker_Close(T *testing.T) {
	T.Parallel()

	T.Run("closes and drops outstanding locks", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, l.Close())
		// The previous handle now sees the lock as not-held.
		must.ErrorIs(t, lock.Release(t.Context()), distributedlock.ErrLockNotHeld)
		// And the key is acquirable again.
		_, err = l.Acquire(t.Context(), "k", time.Second)
		must.NoError(t, err)
	})
}

func TestLocker_Concurrency(T *testing.T) {
	T.Parallel()

	// The conformance suite races a handful of goroutines for one key; this
	// races two orders of magnitude more, which is what a mutex-and-map wants
	// checked and what a networked backend would only turn into a slow test.
	T.Run("only one goroutine wins per key", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)

		const goroutines = 100
		var winners atomic.Int64
		var wg sync.WaitGroup
		wg.Add(goroutines)
		for range goroutines {
			go func() {
				defer wg.Done()
				if _, err := l.Acquire(t.Context(), "racekey", time.Minute); err == nil {
					winners.Add(1)
				}
			}()
		}
		wg.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})
}

// TestLocker_Conformance runs the shared distributedlock.Locker suite. This
// implementation is the one the suite exists for: it is the double a good deal
// of this repository's tests schedule against, so what it does when a lock
// lapses or is released twice is a claim about redis and postgres, not just
// about itself.
func TestLocker_Conformance(T *testing.T) {
	T.Parallel()

	distributedlocktest.Run(T, func(tb testing.TB) distributedlock.Locker {
		tb.Helper()

		l, err := NewLocker()
		must.NoError(tb, err)
		tb.Cleanup(func() { must.NoError(tb, l.Close()) })

		return l
	},
		// Each Locker owns its own map, so two of them share nothing: this
		// provider coordinates goroutines, not replicas.
		distributedlocktest.WithInstanceLocalStore(),
	)
}

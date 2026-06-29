package memory

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v2/distributedlock"
	"github.com/primandproper/platform-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func newTestLocker(t *testing.T) distributedlock.Locker {
	t.Helper()
	l, err := NewLocker(nil, nil, nil)
	must.NoError(t, err)
	must.NotNil(t, l)
	return l
}

// newRecordingLocker builds a locker with a RecordingObserver swapped in, so a
// test can both drive the locker and assert which fields it observed.
func newRecordingLocker(t *testing.T) (*locker, *observability.RecordingObserver) {
	t.Helper()
	l, err := NewLocker(nil, nil, nil)
	must.NoError(t, err)
	must.NotNil(t, l)

	concrete, ok := l.(*locker)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	concrete.o11y = obs

	return concrete, obs
}

func TestNewLocker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(nil, nil, nil)
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

	T.Run("contended", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		_, err := l.Acquire(t.Context(), "shared", time.Minute)
		must.NoError(t, err)
		_, err = l.Acquire(t.Context(), "shared", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	T.Run("re-acquire after expiry", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		_, err := l.Acquire(t.Context(), "exp", 50*time.Millisecond)
		must.NoError(t, err)
		time.Sleep(80 * time.Millisecond)
		_, err = l.Acquire(t.Context(), "exp", time.Second)
		must.NoError(t, err)
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

	T.Run("rejects negative TTL", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		_, err := l.Acquire(t.Context(), "k", -time.Second)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})
}

func TestLocker_Release(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, lock.Release(t.Context()))
	})

	T.Run("released lock can be reacquired", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		first, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, first.Release(t.Context()))
		_, err = l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
	})

	T.Run("double release returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, lock.Release(t.Context()))
		must.ErrorIs(t, lock.Release(t.Context()), distributedlock.ErrLockNotHeld)
	})

	T.Run("release after expiration returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", 50*time.Millisecond)
		must.NoError(t, err)
		time.Sleep(80 * time.Millisecond)
		must.ErrorIs(t, lock.Release(t.Context()), distributedlock.ErrLockNotHeld)
	})
}

func TestLocker_Refresh(T *testing.T) {
	T.Parallel()

	T.Run("extends TTL", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", 50*time.Millisecond)
		must.NoError(t, err)
		must.NoError(t, lock.Refresh(t.Context(), 5*time.Second))
		// Even after the original TTL elapses, the lock is still held.
		time.Sleep(80 * time.Millisecond)
		_, err = l.Acquire(t.Context(), "k", time.Second)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	T.Run("refresh after expiration returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", 50*time.Millisecond)
		must.NoError(t, err)
		time.Sleep(80 * time.Millisecond)
		must.ErrorIs(t, lock.Refresh(t.Context(), time.Second), distributedlock.ErrLockNotHeld)
	})

	T.Run("rejects invalid TTL", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t)
		lock, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, lock.Refresh(t.Context(), 0), distributedlock.ErrInvalidTTL)
	})
}

func TestLocker_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		must.NoError(t, newTestLocker(t).Ping(t.Context()))
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

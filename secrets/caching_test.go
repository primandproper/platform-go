package secrets

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// fakeSource is a scriptable SecretSource. Values, the error every lookup
// returns, and the call count are all read and written under one mutex so a
// test goroutine can rewrite the backend while the refresh is reading it.
type fakeSource struct {
	values   map[string]string
	err      error
	block    chan struct{}
	closeErr error
	mu       sync.Mutex
	calls    int
	closes   int
}

func newFakeSource(values map[string]string) *fakeSource {
	return &fakeSource{values: values}
}

func (f *fakeSource) GetSecret(_ context.Context, name string) (string, error) {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()

	if block != nil {
		<-block
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return "", f.err
	}

	value, ok := f.values[name]
	if !ok {
		return "", ErrSecretNotFound
	}

	return value, nil
}

func (f *fakeSource) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closes++

	return f.closeErr
}

func (f *fakeSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

func (f *fakeSource) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.closes
}

func (f *fakeSource) set(name, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.values[name] = value
}

func (f *fakeSource) remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.values, name)
}

func (f *fakeSource) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.err = err
}

func (f *fakeSource) blockOn(ch chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.block = ch
}

func TestNewCachingSource(T *testing.T) {
	T.Parallel()

	T.Run("requires a source", func(t *testing.T) {
		t.Parallel()

		source, err := NewCachingSource(nil, time.Minute)

		must.Error(t, err)
		test.Nil(t, source)
	})

	T.Run("rejects a non-positive ttl", func(t *testing.T) {
		t.Parallel()

		for _, ttl := range []time.Duration{0, -time.Second} {
			source, err := NewCachingSource(newFakeSource(nil), ttl)

			must.Error(t, err)
			test.ErrorIs(t, err, ErrInvalidCacheTTL)
			test.Nil(t, source)
		}
	})

	T.Run("rejects a refresh interval that cannot beat the ttl", func(t *testing.T) {
		t.Parallel()

		for _, interval := range []time.Duration{time.Minute, 2 * time.Minute} {
			source, err := NewCachingSource(newFakeSource(nil), time.Minute, WithRefresh(context.Background(), interval))

			must.Error(t, err)
			test.ErrorIs(t, err, ErrInvalidRefreshInterval)
			test.Nil(t, source)
		}
	})

	T.Run("builds without a refresh", func(t *testing.T) {
		t.Parallel()

		source, err := NewCachingSource(newFakeSource(nil), time.Minute)

		must.NoError(t, err)
		must.NotNil(t, source)
		must.NoError(t, source.Close())
	})
}

func TestCachingSource_GetSecret(T *testing.T) {
	T.Parallel()

	T.Run("serves repeat reads from the cache", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{"api-key": "one"})
		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = source.Close() })

		for range 5 {
			value, getErr := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, getErr)
			test.EqOp(t, "one", value)
		}

		test.EqOp(t, 1, backend.callCount())
	})

	T.Run("re-reads once the ttl has passed", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			value, err := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			// Just short of the deadline the entry is still good, so nothing
			// reaches the backend.
			time.Sleep(59 * time.Second)
			backend.set("api-key", "two")

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)
			test.EqOp(t, 1, backend.callCount())

			// Across it, the read pays for the round-trip and sees the change.
			time.Sleep(2 * time.Second)

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "two", value)
			test.EqOp(t, 2, backend.callCount())
		})
	})

	T.Run("collapses a cold-key stampede into one backend call", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{"api-key": "one"})
		release := make(chan struct{})
		backend.blockOn(release)

		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = source.Close() })

		const readers = 32

		var (
			wg      sync.WaitGroup
			started sync.WaitGroup
			failed  atomic.Int64
		)

		wg.Add(readers)
		started.Add(readers)

		for range readers {
			go func() {
				defer wg.Done()

				started.Done()

				value, getErr := source.GetSecret(t.Context(), "api-key")
				if getErr != nil || value != "one" {
					failed.Add(1)
				}
			}()
		}

		started.Wait()
		close(release)
		wg.Wait()

		test.EqOp(t, int64(0), failed.Load())
		// One call for the flight the readers shared. A reader that arrived
		// after it finished re-reads the filled entry rather than starting a
		// second flight, so this is exact rather than merely "fewer than 32".
		test.EqOp(t, 1, backend.callCount())
	})

	T.Run("does not cache absence", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{})
		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = source.Close() })

		for range 3 {
			_, getErr := source.GetSecret(t.Context(), "api-key")
			test.ErrorIs(t, getErr, ErrSecretNotFound)
		}

		test.EqOp(t, 3, backend.callCount())

		// A secret created after the source started is visible on the next
		// read, not a TTL later.
		backend.set("api-key", "one")

		value, err := source.GetSecret(t.Context(), "api-key")
		must.NoError(t, err)
		test.EqOp(t, "one", value)
	})

	T.Run("a deleted secret stops being served", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			value, err := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			backend.remove("api-key")
			time.Sleep(2 * time.Minute)

			// Not-found is an answer, not a failed lookup, so the held value is
			// dropped rather than served stale.
			_, err = source.GetSecret(t.Context(), "api-key")
			test.ErrorIs(t, err, ErrSecretNotFound)

			_, err = source.GetSecret(t.Context(), "api-key")
			test.ErrorIs(t, err, ErrSecretNotFound)
		})
	})

	T.Run("serves the held value when the backend fails", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			value, err := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			unreachable := errors.New("secret manager is unreachable")
			backend.fail(unreachable)
			time.Sleep(2 * time.Minute)

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			// Once the backend answers again the fresh value wins.
			backend.fail(nil)
			backend.set("api-key", "two")

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "two", value)
		})
	})

	T.Run("a failing backend with nothing held is an error", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{"api-key": "one"})
		unreachable := errors.New("secret manager is unreachable")
		backend.fail(unreachable)

		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = source.Close() })

		_, err = source.GetSecret(t.Context(), "api-key")
		test.ErrorIs(t, err, unreachable)
	})

	T.Run("a caller whose context ends stops waiting", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{"api-key": "one"})
		release := make(chan struct{})
		backend.blockOn(release)
		t.Cleanup(func() { close(release) })

		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err = source.GetSecret(ctx, "api-key")
		test.ErrorIs(t, err, context.Canceled)
	})
}

func TestCachingSource_Refresh(T *testing.T) {
	T.Parallel()

	T.Run("keeps a held secret warm without a caller blocking", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			value, err := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			backend.set("api-key", "two")

			// A full interval is enough for one refresh however the jitter
			// landed, since jitter only ever shortens the wait.
			time.Sleep(30 * time.Second)
			synctest.Wait()

			// Served from the cache — the entry was refreshed in the
			// background, so this read did not go to the backend itself.
			before := backend.callCount()

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "two", value)
			test.EqOp(t, before, backend.callCount())
		})
	})

	T.Run("refreshes nothing that has not been read", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			time.Sleep(5 * time.Minute)
			synctest.Wait()

			test.EqOp(t, 0, backend.callCount())
		})
	})

	T.Run("a failed refresh leaves the held value in place", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			value, err := source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)

			backend.fail(errors.New("secret manager is unreachable"))

			time.Sleep(5 * time.Minute)
			synctest.Wait()

			value, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)
			test.EqOp(t, "one", value)
		})
	})

	T.Run("stops when its context is done", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})

			ctx, cancel := context.WithCancel(t.Context())

			source, err := NewCachingSource(backend, time.Minute, WithRefresh(ctx, 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			cancel()
			synctest.Wait()

			before := backend.callCount()

			time.Sleep(10 * time.Minute)
			synctest.Wait()

			test.EqOp(t, before, backend.callCount())
		})
	})
}

func TestCachingSource_OnChange(T *testing.T) {
	T.Parallel()

	T.Run("fires when a refresh observes a new value", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			changes := make(chan [2]string, 4)
			source.OnChange("api-key", func(oldValue, newValue string) {
				changes <- [2]string{oldValue, newValue}
			})

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			// Registered but unchanged: a refresh that sees the same value is
			// not a rotation.
			time.Sleep(time.Minute)
			synctest.Wait()
			test.SliceEmpty(t, drain(changes))

			backend.set("api-key", "two")

			time.Sleep(30 * time.Second)
			synctest.Wait()

			observed := drain(changes)
			must.SliceLen(t, 1, observed)
			test.EqOp(t, "one", observed[0][0])
			test.EqOp(t, "two", observed[0][1])
		})
	})

	T.Run("fires when a read observes a new value", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			changes := make(chan [2]string, 4)
			source.OnChange("api-key", func(oldValue, newValue string) {
				changes <- [2]string{oldValue, newValue}
			})

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			backend.set("api-key", "two")
			time.Sleep(2 * time.Minute)

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			synctest.Wait()

			observed := drain(changes)
			must.SliceLen(t, 1, observed)
			test.EqOp(t, "one", observed[0][0])
			test.EqOp(t, "two", observed[0][1])
		})
	})

	T.Run("every hook registered for a name fires", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			var fired atomic.Int64
			for range 3 {
				source.OnChange("api-key", func(_, _ string) { fired.Add(1) })
			}
			source.OnChange("other-key", func(_, _ string) { fired.Add(100) })

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			backend.set("api-key", "two")
			time.Sleep(2 * time.Minute)

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			synctest.Wait()

			test.EqOp(t, int64(3), fired.Load())
		})
	})

	T.Run("cancel unregisters the hook", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			var fired atomic.Int64
			cancel := source.OnChange("api-key", func(_, _ string) { fired.Add(1) })

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			cancel()
			backend.set("api-key", "two")
			time.Sleep(2 * time.Minute)

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			synctest.Wait()

			test.EqOp(t, int64(0), fired.Load())
		})
	})

	T.Run("a nil hook registers nothing and cancels safely", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(map[string]string{"api-key": "one"})
		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = source.Close() })

		cancel := source.OnChange("api-key", nil)
		must.NotNil(t, cancel)
		cancel()
	})

	T.Run("a panicking hook does not take down the refresh", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)
			t.Cleanup(func() { _ = source.Close() })

			var survived atomic.Int64

			source.OnChange("api-key", func(_, _ string) { panic("rebuilding the keyring") })
			source.OnChange("api-key", func(_, _ string) { survived.Add(1) })

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			backend.set("api-key", "two")
			time.Sleep(30 * time.Second)
			synctest.Wait()

			test.EqOp(t, int64(1), survived.Load())

			// The refresh is still running: a second rotation is still seen.
			backend.set("api-key", "three")
			time.Sleep(30 * time.Second)
			synctest.Wait()

			test.EqOp(t, int64(2), survived.Load())
		})
	})
}

func TestCachingSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("closes the wrapped source exactly once", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(nil)
		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)

		must.NoError(t, source.Close())
		must.NoError(t, source.Close())

		test.EqOp(t, 1, backend.closeCount())
	})

	T.Run("reports the wrapped source's error every time", func(t *testing.T) {
		t.Parallel()

		backend := newFakeSource(nil)
		backend.closeErr = errors.New("closing the client")

		source, err := NewCachingSource(backend, time.Minute)
		must.NoError(t, err)

		test.ErrorIs(t, source.Close(), backend.closeErr)
		test.ErrorIs(t, source.Close(), backend.closeErr)
	})

	T.Run("stops the refresh", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			backend := newFakeSource(map[string]string{"api-key": "one"})
			source, err := NewCachingSource(backend, time.Minute, WithRefresh(t.Context(), 30*time.Second))
			must.NoError(t, err)

			_, err = source.GetSecret(t.Context(), "api-key")
			must.NoError(t, err)

			must.NoError(t, source.Close())

			before := backend.callCount()

			time.Sleep(10 * time.Minute)
			synctest.Wait()

			test.EqOp(t, before, backend.callCount())
		})
	})
}

// drain empties ch without blocking, so a test can assert on what has been
// delivered so far.
func drain[T any](ch chan T) []T {
	var out []T

	for {
		select {
		case v := <-ch:
			out = append(out, v)
		default:
			return out
		}
	}
}

package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// countingLoader answers with the key it was handed and records how many times
// it was actually called, which is the only thing that says whether concurrent
// misses collapsed.
type countingLoader struct {
	calls atomic.Int64
}

func (l *countingLoader) load(_ context.Context, key string) (*example, error) {
	l.calls.Add(1)

	return &example{Name: key}, nil
}

// newLoadingCache builds a read-through cache over loader.
func newLoadingCache(t *testing.T, loader Loader[example], opts ...Option) *Cache[example] {
	t.Helper()

	c, err := NewInMemoryCache[example](time.Hour, append([]Option{WithLoader(loader)}, opts...)...)
	must.NoError(t, err)

	return c
}

func TestWithLoader(T *testing.T) {
	T.Parallel()

	T.Run("a miss loads, stores, and returns", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		loader := &countingLoader{}
		c := newLoadingCache(t, loader.load)

		got, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, exampleKey, got.Name)
		test.EqOp(t, int64(1), loader.calls.Load())

		// Stored, not merely returned: the second read is served from the map.
		got, err = c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, exampleKey, got.Name)
		test.EqOp(t, int64(1), loader.calls.Load())
	})

	T.Run("a hit does not reach the loader", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		loader := &countingLoader{}
		c := newLoadingCache(t, loader.load)

		must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "written"}))

		got, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, "written", got.Name)
		test.EqOp(t, int64(0), loader.calls.Load())
	})

	T.Run("an expired entry is reloaded rather than served", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			loader := &countingLoader{}
			c := newLoadingCache(t, loader.load)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "stale"}, cache.WithExpiry(time.Second)))
			time.Sleep(2 * time.Second)

			got, err := c.Get(ctx, exampleKey)
			must.NoError(t, err)
			test.EqOp(t, exampleKey, got.Name)
			test.EqOp(t, int64(1), loader.calls.Load())
		})
	})

	T.Run("without a loader a miss is still a miss", func(t *testing.T) {
		t.Parallel()

		c := newExpiryCache(t, time.Hour)

		_, err := c.Get(t.Context(), exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
	})

	T.Run("a nil loader leaves the cache unchanged", func(t *testing.T) {
		t.Parallel()

		c, err := NewInMemoryCache[example](time.Hour, WithLoader[example](nil))
		must.NoError(t, err)

		test.Nil(t, c.loader)

		_, err = c.Get(t.Context(), exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
	})

	T.Run("a loader for another type fails construction", func(t *testing.T) {
		t.Parallel()

		// Option cannot name the cached type, so the mismatch cannot be a
		// compile error; it has to be caught here rather than silently leaving
		// the cache without the loader it was given.
		_, err := NewInMemoryCache[example](time.Hour, WithLoader(func(context.Context, string) (*string, error) {
			return nil, nil
		}))
		test.ErrorIs(t, err, ErrLoaderTypeMismatch)
	})
}

func TestWithLoader_Errors(T *testing.T) {
	T.Parallel()

	T.Run("a loader reporting absence is a miss, and caches nothing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		var calls atomic.Int64

		c := newLoadingCache(t, func(context.Context, string) (*example, error) {
			calls.Add(1)

			return nil, cache.ErrNotFound
		})

		_, err := c.Get(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.EqOp(t, 0, size(c))

		// Absence is not cached, so the next read asks again.
		_, err = c.Get(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.EqOp(t, int64(2), calls.Load())
	})

	T.Run("a failing loader caches nothing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		boom := errors.New("aggregate query failed")

		c := newLoadingCache(t, func(context.Context, string) (*example, error) {
			return nil, boom
		})

		_, err := c.Get(ctx, exampleKey)
		test.ErrorIs(t, err, boom)
		test.EqOp(t, 0, size(c))
	})

	T.Run("a nil value is a value", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		var calls atomic.Int64

		c := newLoadingCache(t, func(context.Context, string) (*example, error) {
			calls.Add(1)

			return nil, nil
		})

		got, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.Nil(t, got)

		// Stored like any other value: "the answer is nothing" is cacheable,
		// which is what separates it from cache.ErrNotFound.
		got, err = c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.Nil(t, got)
		test.EqOp(t, int64(1), calls.Load())
	})
}

func TestWithLoader_Singleflight(T *testing.T) {
	T.Parallel()

	T.Run("concurrent misses on one key produce one load", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			release := make(chan struct{})

			var calls atomic.Int64

			c := newLoadingCache(t, func(_ context.Context, key string) (*example, error) {
				calls.Add(1)
				<-release

				return &example{Name: key}, nil
			})

			const readers = 8

			var (
				wg      sync.WaitGroup
				results = make([]*example, readers)
				errs    = make([]error, readers)
			)

			for idx := range readers {
				wg.Go(func() {
					results[idx], errs[idx] = c.Get(ctx, exampleKey)
				})
			}

			// Every reader is now parked: one inside the loader, the rest
			// waiting on the flight it started. Without the bubble this would
			// be a sleep long enough to be flaky in both directions.
			synctest.Wait()
			test.EqOp(t, int64(1), calls.Load())

			close(release)
			wg.Wait()

			for idx := range readers {
				must.NoError(t, errs[idx])
				test.EqOp(t, exampleKey, results[idx].Name)
			}

			test.EqOp(t, int64(1), calls.Load())
		})
	})

	T.Run("concurrent misses on different keys do not collapse", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()

			var calls atomic.Int64

			c := newLoadingCache(t, func(_ context.Context, key string) (*example, error) {
				calls.Add(1)

				return &example{Name: key}, nil
			})

			keys := []string{"a", "b", "c"}

			var wg sync.WaitGroup

			for _, key := range keys {
				wg.Go(func() {
					_, err := c.Get(ctx, key)
					test.NoError(t, err)
				})
			}

			wg.Wait()

			test.EqOp(t, int64(len(keys)), calls.Load())
		})
	})

	T.Run("a caller giving up does not cancel the load others are waiting on", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})

			c := newLoadingCache(t, func(loadCtx context.Context, key string) (*example, error) {
				<-release

				// The load outlives the caller that started it, so it must not
				// be running on that caller's context.
				if err := loadCtx.Err(); err != nil {
					return nil, err
				}

				return &example{Name: key}, nil
			})

			leaverCtx, leave := context.WithCancel(t.Context())

			var (
				wg        sync.WaitGroup
				leaverErr error
				waiterVal *example
				waiterErr error
			)

			// Launched one at a time, each followed by a Wait, so the leaver is
			// definitely the caller that started the flight and the waiter is
			// definitely one that joined it.
			wg.Go(func() {
				_, leaverErr = c.Get(leaverCtx, exampleKey)
			})

			synctest.Wait()

			wg.Go(func() {
				waiterVal, waiterErr = c.Get(t.Context(), exampleKey)
			})

			synctest.Wait()

			// The caller that started the flight walks away.
			leave()
			synctest.Wait()

			close(release)
			wg.Wait()

			test.ErrorIs(t, leaverErr, context.Canceled)
			must.NoError(t, waiterErr)
			test.EqOp(t, exampleKey, waiterVal.Name)
		})
	})

	T.Run("counts every miss but only the loads it ran", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			release := make(chan struct{})

			c := newLoadingCache(t, func(_ context.Context, key string) (*example, error) {
				<-release

				return &example{Name: key}, nil
			})

			misses, loads := &countingCounter{}, &countingCounter{}
			c.cacheMissCounter = misses
			c.cacheLoadCounter = loads

			const readers = 4

			var wg sync.WaitGroup

			for range readers {
				wg.Go(func() {
					_, err := c.Get(ctx, exampleKey)
					test.NoError(t, err)
				})
			}

			synctest.Wait()
			close(release)
			wg.Wait()

			// Misses minus loads is what the flight saved.
			test.EqOp(t, int64(readers), misses.Total())
			test.EqOp(t, int64(1), loads.Total())
		})
	})
}

func TestWithLoader_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("loads what the batch missed and keeps what it hit", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		loader := &countingLoader{}
		c := newLoadingCache(t, loader.load)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "written"}))

		got, err := c.GetMany(ctx, []string{"a", "b", "c"})
		must.NoError(t, err)
		must.MapLen(t, 3, got)

		test.EqOp(t, "written", got["a"].Name)
		test.EqOp(t, "b", got["b"].Name)
		test.EqOp(t, "c", got["c"].Name)
		test.EqOp(t, int64(2), loader.calls.Load())
	})

	T.Run("duplicate keys join one load", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			loader := &countingLoader{}
			c := newLoadingCache(t, loader.load)

			got, err := c.GetMany(ctx, []string{exampleKey, exampleKey, exampleKey})
			must.NoError(t, err)
			must.MapLen(t, 1, got)

			test.EqOp(t, int64(1), loader.calls.Load())
		})
	})

	T.Run("a key the loader cannot find is omitted, not an error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		c := newLoadingCache(t, func(_ context.Context, key string) (*example, error) {
			if key == "missing" {
				return nil, cache.ErrNotFound
			}

			return &example{Name: key}, nil
		})

		got, err := c.GetMany(ctx, []string{"a", "missing"})
		must.NoError(t, err)
		must.MapLen(t, 1, got)
		test.EqOp(t, "a", got["a"].Name)
	})

	T.Run("a failing load fails the call", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		boom := errors.New("aggregate query failed")

		c := newLoadingCache(t, func(_ context.Context, key string) (*example, error) {
			if key == "broken" {
				return nil, boom
			}

			return &example{Name: key}, nil
		})

		// A partial map would be indistinguishable from a partial miss, which
		// is exactly what a GetMany caller cannot detect.
		got, err := c.GetMany(ctx, []string{"a", "broken"})
		test.ErrorIs(t, err, boom)
		test.Nil(t, got)
	})

	T.Run("without a loader a batch still reports misses by omission", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newExpiryCache(t, time.Hour)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))

		got, err := c.GetMany(ctx, []string{"a", "b"})
		must.NoError(t, err)
		test.MapLen(t, 1, got)
	})
}

// TestWithLoader_BoundedCache covers the two options together: a read-through
// cache is the one most likely to want a bound, since a loader will answer for
// any key at all.
func TestWithLoader_BoundedCache(T *testing.T) {
	T.Parallel()

	T.Run("loaded entries are subject to the bound", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		loader := &countingLoader{}
		c := newLoadingCache(t, loader.load, WithMaxEntries(2, EvictOldestWritten))

		for _, key := range []string{"a", "b", "c", "d"} {
			_, err := c.Get(ctx, key)
			must.NoError(t, err)
		}

		test.EqOp(t, 2, size(c))
	})
}

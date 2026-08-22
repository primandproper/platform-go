package memory

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const (
	exampleKey = "example"
)

type example struct {
	Name string `json:"name"`
}

// newRecordingCache builds an in-memory cache with a RecordingObserver swapped
// in, so a test can drive a method and assert that it opened and ended an
// operation.
func newRecordingCache(t *testing.T) (*Cache[example], *observability.RecordingObserver) {
	t.Helper()

	c, err := NewInMemoryCache[example](0)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func Test_newInMemoryCache(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		actual, err := NewInMemoryCache[example](0)
		must.NoError(t, err)
		test.NotNil(t, actual)
	})
}

func Test_Cache_Get(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		expected := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, expected))

		actual, err := c.Get(ctx, exampleKey)
		test.Eq(t, expected, actual)
		test.NoError(t, err)
	})

	T.Run("observes operation", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, obs := newRecordingCache(t)

		expected := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, expected))

		actual, err := c.Get(ctx, exampleKey)
		test.Eq(t, expected, actual)
		test.NoError(t, err)

		// The cache methods attach no values, so assert the operation
		// lifecycle: Get opened and ended an operation with no errors.
		op := obs.ObservedOperationWithKeys(t)
		must.SliceEmpty(t, op.Errors)
	})
}

func Test_Cache_Set(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		test.MapLen(t, 0, c.cache)
		test.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
		test.MapLen(t, 1, c.cache)
	})
}

func Test_Cache_SetIfPresent(T *testing.T) {
	T.Parallel()

	T.Run("overwrites an entry that is there", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "before"}))
		must.NoError(t, c.SetIfPresent(ctx, exampleKey, &example{Name: "after"}))

		got, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, "after", got.Name)
	})

	T.Run("refuses an entry that is absent, and does not create it", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		test.ErrorIs(t, c.SetIfPresent(ctx, exampleKey, &example{Name: t.Name()}), cache.ErrNotFound)

		// The refusal has to leave the map alone. A conditional write that
		// creates on miss is just Set with extra steps, and the caller reaching
		// for it is relying on the absence being preserved.
		test.MapLen(t, 0, c.cache)
	})

	T.Run("refuses an entry whose deadline has passed", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c, err := NewInMemoryCache[example](time.Minute)
			must.NoError(t, err)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "before"}))

			time.Sleep(2 * time.Minute)
			synctest.Wait()

			// Still in the map — nothing has swept it — but expired, and the
			// caller asking "is it still there" means the deadline.
			test.MapLen(t, 1, c.cache)
			test.ErrorIs(t, c.SetIfPresent(ctx, exampleKey, &example{Name: "after"}), cache.ErrNotFound)
		})
	})

	T.Run("a delete racing a conditional write cannot be undone", func(t *testing.T) {
		t.Parallel()

		// The property the method exists for. Under a read-then-write, a Delete
		// landing between the two steps is silently reversed; here the write
		// either precedes the delete or is refused by it, so the entry is never
		// resurrected. Run under -race, which is how the suite runs.
		ctx := t.Context()

		for range 200 {
			c, err := NewInMemoryCache[example](0)
			must.NoError(t, err)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "before"}))

			var wg sync.WaitGroup

			wg.Add(2)

			go func() {
				defer wg.Done()

				// The racing half; its outcome is not what is asserted.
				_ = c.Delete(ctx, exampleKey)
			}()

			var writeErr error

			go func() {
				defer wg.Done()

				writeErr = c.SetIfPresent(ctx, exampleKey, &example{Name: "after"})
			}()

			wg.Wait()

			// If the write was refused, the delete won and the entry must be
			// gone. That is the direction that matters: a refused conditional
			// write must never leave the value behind.
			if writeErr != nil {
				must.ErrorIs(t, writeErr, cache.ErrNotFound)
				_, getErr := c.Get(ctx, exampleKey)
				must.ErrorIs(t, getErr, cache.ErrNotFound)
			}
		}
	})
}

func Test_Cache_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		test.MapLen(t, 0, c.cache)
		test.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
		test.MapLen(t, 1, c.cache)
		test.NoError(t, c.Delete(ctx, exampleKey))
		test.MapLen(t, 0, c.cache)
	})
}

func Test_Cache_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("returns only hits", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		hit := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, "hit", hit))

		bc := c
		out, getErr := bc.GetMany(ctx, []string{"hit", "miss"})
		test.NoError(t, getErr)
		test.MapLen(t, 1, out)
		test.Eq(t, hit, out["hit"])
	})

	T.Run("empty keys", func(t *testing.T) {
		t.Parallel()

		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		out, getErr := c.GetMany(t.Context(), nil)
		test.NoError(t, getErr)
		test.MapLen(t, 0, out)
	})
}

func Test_Cache_SetMany(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)

		bc := c
		test.MapLen(t, 0, bc.cache)

		test.NoError(t, bc.SetMany(ctx, map[string]*example{
			"a": {Name: "a"},
			"b": {Name: "b"},
		}))
		test.MapLen(t, 2, bc.cache)
	})
}

func Test_Cache_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, err := NewInMemoryCache[example](0)
		must.NoError(t, err)
		test.NoError(t, c.Ping(t.Context()))
	})
}

// newExpiryCache builds a cache with the given default expiry. The expiry
// tests run inside a synctest bubble, where the production clock reads the
// bubble's fake time, so time.Sleep moves expiry forward without a real wait.
func newExpiryCache(t *testing.T, defaultExpiry time.Duration) *Cache[example] {
	t.Helper()

	c, err := NewInMemoryCache[example](defaultExpiry)
	must.NoError(t, err)

	return c
}

func TestInMemoryCache_Expiry(T *testing.T) {
	T.Parallel()

	T.Run("entries expire after the default expiry", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Minute)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))

			// Bubble time lands on the deadline exactly, so the boundary itself
			// is testable: live a nanosecond before, gone a nanosecond later.
			time.Sleep(time.Minute - time.Nanosecond)
			_, err := c.Get(ctx, exampleKey)
			must.NoError(t, err)

			time.Sleep(time.Nanosecond)
			_, err = c.Get(ctx, exampleKey)
			test.ErrorIs(t, err, cache.ErrNotFound)
		})
	})

	T.Run("WithExpiry overrides the default per call", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Minute)

			must.NoError(t, c.Set(ctx, "short", &example{Name: "short"}, cache.WithExpiry(time.Second)))
			must.NoError(t, c.Set(ctx, "long", &example{Name: "long"}, cache.WithExpiry(time.Hour)))

			time.Sleep(time.Minute)

			_, err := c.Get(ctx, "short")
			test.ErrorIs(t, err, cache.ErrNotFound)

			_, err = c.Get(ctx, "long")
			test.NoError(t, err)
		})
	})

	T.Run("NoExpiry pins an entry against expiry", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Minute)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}, cache.WithExpiry(cache.NoExpiry)))

			time.Sleep(1000 * time.Hour)

			_, err := c.Get(ctx, exampleKey)
			test.NoError(t, err)
		})
	})

	T.Run("non-positive default means entries never expire", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, 0)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))

			time.Sleep(1000 * time.Hour)

			_, err := c.Get(ctx, exampleKey)
			test.NoError(t, err)
		})
	})

	T.Run("SetMany applies one expiry to the batch and GetMany filters expired entries", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Minute)

			must.NoError(t, c.SetMany(ctx, map[string]*example{
				"a": {Name: "a"},
				"b": {Name: "b"},
			}, cache.WithExpiry(time.Second)))
			must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

			time.Sleep(time.Second)

			out, err := c.GetMany(ctx, []string{"a", "b", "c"})
			must.NoError(t, err)
			must.MapLen(t, 1, out)
			test.EqOp(t, "c", out["c"].Name)
		})
	})

	T.Run("expired entries are evicted lazily on read", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
			time.Sleep(time.Second)

			_, err := c.Get(ctx, exampleKey)
			test.ErrorIs(t, err, cache.ErrNotFound)

			c.cacheMu.RLock()
			_, stillPresent := c.cache[exampleKey]
			c.cacheMu.RUnlock()
			test.False(t, stillPresent)
		})
	})

	T.Run("overwriting an expired entry revives the key", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c := newExpiryCache(t, time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "old"}))
			time.Sleep(time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "new"}))

			got, err := c.Get(ctx, exampleKey)
			must.NoError(t, err)
			test.EqOp(t, "new", got.Name)
		})
	})
}

// countingCounter records what an instrument was asked to add, so a test can
// assert a counter fired without standing up an SDK metrics pipeline.
type countingCounter struct {
	mu    sync.Mutex
	total int64
}

func (c *countingCounter) Add(_ context.Context, incr int64, _ ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total += incr
}

func (c *countingCounter) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.total
}

// newEvictionCountingCache builds an expiry cache with its eviction counter
// swapped for one the test can read back.
func newEvictionCountingCache(t *testing.T, defaultExpiry time.Duration) (*Cache[example], *countingCounter) {
	t.Helper()

	c := newExpiryCache(t, defaultExpiry)
	counter := &countingCounter{}
	c.cacheEvictCounter = counter

	return c, counter
}

func TestInMemoryCache_EvictionCounter(T *testing.T) {
	T.Parallel()

	T.Run("counts an entry dropped by the read that discovers it expired", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c, evictions := newEvictionCountingCache(t, time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
			test.EqOp(t, int64(0), evictions.Total())

			time.Sleep(time.Second)

			_, err := c.Get(ctx, exampleKey)
			test.ErrorIs(t, err, cache.ErrNotFound)
			test.EqOp(t, int64(1), evictions.Total())
		})
	})

	T.Run("counts each expired key GetMany discovers", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c, evictions := newEvictionCountingCache(t, time.Second)

			must.NoError(t, c.SetMany(ctx, map[string]*example{
				"a": {Name: "a"},
				"b": {Name: "b"},
				"c": {Name: "c"},
			}))
			time.Sleep(time.Second)

			got, err := c.GetMany(ctx, []string{"a", "b", "c"})
			must.NoError(t, err)
			test.MapEmpty(t, got)
			test.EqOp(t, int64(3), evictions.Total())
		})
	})

	T.Run("a miss on an absent key is not an eviction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, evictions := newEvictionCountingCache(t, time.Minute)

		_, err := c.Get(ctx, "never-written")
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.EqOp(t, int64(0), evictions.Total())
	})

	T.Run("a live entry read before its deadline is not an eviction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, evictions := newEvictionCountingCache(t, time.Minute)

		must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))

		_, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, int64(0), evictions.Total())
	})

	T.Run("overwriting an expired entry is a write, not an eviction", func(t *testing.T) {
		t.Parallel()

		// Documents the deliberate gap on evictExpired: a Set that lands on an
		// expired entry before any read discovers it replaces it silently. The
		// counter answers "how much am I losing to TTL", and folding overwrites
		// in would double-count against cacheSetCounter.
		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c, evictions := newEvictionCountingCache(t, time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "old"}))
			time.Sleep(time.Second)
			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "new"}))

			test.EqOp(t, int64(0), evictions.Total())
		})
	})

	T.Run("an explicit Delete of an expired entry is a delete, not an eviction", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			c, evictions := newEvictionCountingCache(t, time.Second)

			must.NoError(t, c.Set(ctx, exampleKey, &example{Name: t.Name()}))
			time.Sleep(time.Second)
			must.NoError(t, c.Delete(ctx, exampleKey))

			test.EqOp(t, int64(0), evictions.Total())
		})
	})
}

func TestInMemoryCache_Deletion(T *testing.T) {
	T.Parallel()

	T.Run("DeleteMany removes only the named keys", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newExpiryCache(t, 0)

		must.NoError(t, c.SetMany(ctx, map[string]*example{
			"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"},
		}))

		must.NoError(t, c.DeleteMany(ctx, []string{"a", "b", "missing"}))

		out, err := c.GetMany(ctx, []string{"a", "b", "c"})
		must.NoError(t, err)
		must.MapLen(t, 1, out)
		test.EqOp(t, "c", out["c"].Name)
	})

	T.Run("DeleteByPrefix removes matching keys only", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newExpiryCache(t, 0)

		must.NoError(t, c.SetMany(ctx, map[string]*example{
			"area:1:x": {Name: "1x"}, "area:1:y": {Name: "1y"}, "area:2:x": {Name: "2x"},
		}))

		must.NoError(t, c.DeleteByPrefix(ctx, "area:1:"))

		out, err := c.GetMany(ctx, []string{"area:1:x", "area:1:y", "area:2:x"})
		must.NoError(t, err)
		must.MapLen(t, 1, out)
		test.EqOp(t, "2x", out["area:2:x"].Name)
	})

	T.Run("Flush clears everything", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newExpiryCache(t, 0)

		must.NoError(t, c.SetMany(ctx, map[string]*example{
			"a": {Name: "a"}, "b": {Name: "b"},
		}))

		must.NoError(t, c.Flush(ctx))

		out, err := c.GetMany(ctx, []string{"a", "b"})
		must.NoError(t, err)
		must.MapLen(t, 0, out)
	})
}

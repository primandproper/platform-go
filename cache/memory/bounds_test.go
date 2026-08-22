package memory

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newBoundedCache builds a cache bounded to maxEntries under policy, with a
// default expiry long enough that nothing under test expires on its own.
func newBoundedCache(t *testing.T, maxEntries int, policy EvictionPolicy) *Cache[example] {
	t.Helper()

	c, err := NewInMemoryCache[example](time.Hour, WithMaxEntries(maxEntries, policy))
	must.NoError(t, err)

	return c
}

// resident reports which of keys the cache still holds, so a test can name the
// survivors rather than counting them.
func resident(t *testing.T, c *Cache[example], keys ...string) []string {
	t.Helper()

	var out []string

	for _, key := range keys {
		if _, err := c.Get(t.Context(), key); err == nil {
			out = append(out, key)
		}
	}

	return out
}

func TestEvictionPolicy(T *testing.T) {
	T.Parallel()

	T.Run("names round-trip through ParseEvictionPolicy", func(t *testing.T) {
		t.Parallel()

		for _, policy := range []EvictionPolicy{EvictLeastRecentlyUsed, EvictOldestWritten} {
			parsed, err := ParseEvictionPolicy(policy.String())
			must.NoError(t, err)
			test.EqOp(t, policy, parsed)
		}
	})

	T.Run("accepts shorthands, spacing, and case", func(t *testing.T) {
		t.Parallel()

		for name, expected := range map[string]EvictionPolicy{
			"lru":                   EvictLeastRecentlyUsed,
			"  LRU  ":               EvictLeastRecentlyUsed,
			"Least_Recently_Used":   EvictLeastRecentlyUsed,
			"fifo":                  EvictOldestWritten,
			"OLDEST_WRITTEN":        EvictOldestWritten,
			" oldest_written      ": EvictOldestWritten,
		} {
			parsed, err := ParseEvictionPolicy(name)
			must.NoError(t, err)
			test.EqOp(t, expected, parsed)
		}
	})

	T.Run("rejects an unknown name rather than defaulting", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"", "random", "least_recently_written"} {
			_, err := ParseEvictionPolicy(name)
			test.ErrorIs(t, err, ErrUnknownEvictionPolicy)
		}
	})

	T.Run("an undefined policy renders as unknown", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "unknown", EvictionPolicy(0).String())
		test.EqOp(t, "unknown", EvictionPolicy(200).String())
	})
}

func TestWithMaxEntries(T *testing.T) {
	T.Parallel()

	T.Run("a non-positive bound leaves the cache unbounded", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		for _, maxEntries := range []int{0, -1} {
			c, err := NewInMemoryCache[example](time.Hour, WithMaxEntries(maxEntries, EvictLeastRecentlyUsed))
			must.NoError(t, err)

			test.Nil(t, c.index)

			for _, key := range []string{"a", "b", "c"} {
				must.NoError(t, c.Set(ctx, key, &example{Name: key}))
			}

			test.EqOp(t, 3, size(c))
		}
	})

	T.Run("an undefined policy fails construction", func(t *testing.T) {
		t.Parallel()

		_, err := NewInMemoryCache[example](time.Hour, WithMaxEntries(10, EvictionPolicy(0)))
		test.ErrorIs(t, err, ErrUnknownEvictionPolicy)
	})

	T.Run("an undefined policy is not read for an unbounded cache", func(t *testing.T) {
		t.Parallel()

		// The option never records the policy when the bound is off, so a
		// configuration that turns a bound off does not also have to correct
		// the policy it left behind.
		c, err := NewInMemoryCache[example](time.Hour, WithMaxEntries(0, EvictionPolicy(200)))
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("holds the bound across writes", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 3, EvictOldestWritten)

		for _, key := range []string{"a", "b", "c", "d", "e", "f"} {
			must.NoError(t, c.Set(ctx, key, &example{Name: key}))
			test.LessEq(t, 3, size(c))
		}

		test.EqOp(t, 3, size(c))
	})

	T.Run("overwriting a key does not grow the cache", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))

		for range 5 {
			must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		}

		test.EqOp(t, 2, size(c))
		test.Eq(t, []string{"a", "b"}, resident(t, c, "a", "b"))
	})

	T.Run("a deleted entry frees room rather than an eviction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))
		must.NoError(t, c.Delete(ctx, "a"))
		must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

		// Had the delete left "a" in the order, adding "c" would have evicted
		// "b" to make room for an entry that is no longer there.
		test.Eq(t, []string{"b", "c"}, resident(t, c, "a", "b", "c"))
	})

	T.Run("SetMany settles on its own tail rather than thrashing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		must.NoError(t, c.SetMany(ctx, map[string]*example{
			"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"}, "d": {Name: "d"},
		}))

		// A map's iteration order decides which two survive, so the assertion
		// is on the count: what matters is that a batch bigger than the bound
		// leaves the cache full rather than empty or over.
		test.EqOp(t, 2, size(c))
	})
}

func TestEvictionPolicy_Order(T *testing.T) {
	T.Parallel()

	T.Run("least recently used keeps what was read", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictLeastRecentlyUsed)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))

		// "a" is the oldest write but the newest read, which is the whole
		// difference between the two policies.
		_, err := c.Get(ctx, "a")
		must.NoError(t, err)

		must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

		test.Eq(t, []string{"a", "c"}, resident(t, c, "a", "b", "c"))
	})

	T.Run("least recently used counts a batch read", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictLeastRecentlyUsed)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))

		got, err := c.GetMany(ctx, []string{"a"})
		must.NoError(t, err)
		must.MapLen(t, 1, got)

		must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

		test.Eq(t, []string{"a", "c"}, resident(t, c, "a", "b", "c"))
	})

	T.Run("oldest written ignores reads", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))

		for range 3 {
			_, err := c.Get(ctx, "a")
			must.NoError(t, err)
		}

		must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

		// Read three times and evicted anyway: under this policy only writes
		// move an entry.
		test.Eq(t, []string{"b", "c"}, resident(t, c, "a", "b", "c"))
	})

	T.Run("oldest written counts an overwrite as a write", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))
		must.NoError(t, c.Set(ctx, "a", &example{Name: "a2"}))
		must.NoError(t, c.Set(ctx, "c", &example{Name: "c"}))

		test.Eq(t, []string{"a", "c"}, resident(t, c, "a", "b", "c"))
	})

	T.Run("only least recently used takes the write lock to read", func(t *testing.T) {
		t.Parallel()

		test.True(t, newBoundedCache(t, 2, EvictLeastRecentlyUsed).index.recordsReads())
		test.False(t, newBoundedCache(t, 2, EvictOldestWritten).index.recordsReads())

		// The unbounded cache is the nil index, which every method tolerates.
		var unbounded *evictionIndex
		test.False(t, unbounded.recordsReads())
		test.Nil(t, unbounded.evictOverflow())
	})
}

func TestBoundedCache_IndexStaysInSyncWithTheMap(T *testing.T) {
	T.Parallel()

	// Every route out of the map has to take the entry out of the order too. An
	// order holding keys the map no longer has silently shrinks the cache: the
	// bound counts ghosts, and real entries are evicted to make room for them.
	fill := func(t *testing.T, c *Cache[example], keys ...string) {
		t.Helper()

		for _, key := range keys {
			must.NoError(t, c.Set(t.Context(), key, &example{Name: key}))
		}
	}

	T.Run("after DeleteMany", func(t *testing.T) {
		t.Parallel()

		c := newBoundedCache(t, 4, EvictOldestWritten)
		fill(t, c, "a", "b", "c")

		must.NoError(t, c.DeleteMany(t.Context(), []string{"a", "b"}))

		test.EqOp(t, 1, c.index.order.Len())
		test.MapLen(t, 1, c.index.elements)
	})

	T.Run("after DeleteByPrefix", func(t *testing.T) {
		t.Parallel()

		c := newBoundedCache(t, 4, EvictOldestWritten)
		fill(t, c, "keep", "drop:1", "drop:2")

		must.NoError(t, c.DeleteByPrefix(t.Context(), "drop:"))

		test.EqOp(t, 1, c.index.order.Len())
		test.MapLen(t, 1, c.index.elements)
	})

	T.Run("after Flush", func(t *testing.T) {
		t.Parallel()

		c := newBoundedCache(t, 4, EvictOldestWritten)
		fill(t, c, "a", "b", "c")

		must.NoError(t, c.Flush(t.Context()))

		test.EqOp(t, 0, c.index.order.Len())
		test.MapLen(t, 0, c.index.elements)
	})

	T.Run("after an expiry eviction", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		c, err := NewInMemoryCache[example](time.Hour, WithMaxEntries(4, EvictOldestWritten))
		must.NoError(t, err)

		must.NoError(t, c.Set(ctx, "a", &example{Name: "a"}, cache.WithExpiry(time.Nanosecond)))
		must.NoError(t, c.Set(ctx, "b", &example{Name: "b"}))

		time.Sleep(time.Millisecond)

		_, err = c.Get(ctx, "a")
		test.ErrorIs(t, err, cache.ErrNotFound)

		test.EqOp(t, 1, c.index.order.Len())
		test.MapLen(t, 1, c.index.elements)
	})
}

func TestBoundedCache_CapacityEvictionCounter(T *testing.T) {
	T.Parallel()

	// Capacity and expiry evictions are counted apart because they call for
	// different fixes: a bound that is too small against a TTL that is too
	// short.
	T.Run("counts capacity evictions separately from expiry evictions", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newBoundedCache(t, 2, EvictOldestWritten)

		capacityEvictions, expiryEvictions := &countingCounter{}, &countingCounter{}
		c.cacheCapacityEvictCounter = capacityEvictions
		c.cacheEvictCounter = expiryEvictions

		for _, key := range []string{"a", "b", "c", "d"} {
			must.NoError(t, c.Set(ctx, key, &example{Name: key}))
		}

		test.EqOp(t, int64(2), capacityEvictions.Total())
		test.EqOp(t, int64(0), expiryEvictions.Total())
	})

	T.Run("stays at zero for an unbounded cache", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c := newExpiryCache(t, time.Hour)

		capacityEvictions := &countingCounter{}
		c.cacheCapacityEvictCounter = capacityEvictions

		for _, key := range []string{"a", "b", "c", "d"} {
			must.NoError(t, c.Set(ctx, key, &example{Name: key}))
		}

		test.EqOp(t, int64(0), capacityEvictions.Total())
	})
}

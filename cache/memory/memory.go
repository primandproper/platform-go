package memory

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"golang.org/x/sync/singleflight"
)

const name = "in_memory_cache"

var _ cache.Cache[struct{}] = (*Cache[struct{}])(nil)

// entry is one stored value; a zero expiresAt means the entry never expires.
type entry[T any] struct {
	expiresAt time.Time
	value     *T
}

// Cache is the in-memory cache.Cache implementation. It is exported, and
// returned by NewInMemoryCache, so a caller can depend on this cache rather
// than on the interface every provider shares: nothing here is unreachable
// without a network, nothing returns cache.ErrUnavailable, and Flush needs no
// namespace, so code built against this type need not carry the handling those
// possibilities force on code built against cache.Cache.
type Cache[T any] struct {
	o11y                      observability.Observer
	logger                    logging.Logger
	tracerProvider            tracing.Provider
	metricsProvider           metrics.Provider
	clock                     clock.Clock
	cacheHitCounter           metrics.Int64Counter
	cacheMissCounter          metrics.Int64Counter
	cacheSetCounter           metrics.Int64Counter
	cacheDelCounter           metrics.Int64Counter
	cacheEvictCounter         metrics.Int64Counter
	cacheCapacityEvictCounter metrics.Int64Counter
	cacheLoadCounter          metrics.Int64Counter
	latencyHist               metrics.Float64Histogram
	// flight collapses concurrent loads for one key. It is only reached when a
	// loader is configured, and is zero-valued and unused otherwise.
	flight        singleflight.Group
	janitor       func()
	stopJanitor   context.CancelFunc
	loader        Loader[T]
	index         *evictionIndex
	cache         map[string]entry[T]
	defaultExpiry time.Duration
	cacheMu       sync.RWMutex
}

// NewInMemoryCache builds an in-memory cache. Writes expire after defaultExpiry
// unless overridden per call with cache.WithExpiry; a non-positive defaultExpiry
// means entries never expire by default.
//
// By default expired entries are evicted lazily, on the read that discovers
// them or when overwritten, and the map is not otherwise size-bounded. Pass
// WithJanitor to sweep them on a timer instead — see that option for when the
// lazy default is not enough — and WithMaxEntries to bound the map by count
// rather than only by expiry.
//
// The cache answers only for what has been written to it unless it is given a
// WithLoader, which makes reads compute what they miss and collapses concurrent
// misses on a key into one computation.
func NewInMemoryCache[T any](defaultExpiry time.Duration, opts ...Option) (*Cache[T], error) {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	// Checked before anything is built: a bound whose policy nobody implements
	// is a cache that would silently forget by some other rule than the one it
	// was configured with.
	if o.maxEntries > 0 && !o.evictionPolicy.valid() {
		return nil, errors.Wrapf(ErrUnknownEvictionPolicy, "eviction policy %d", uint8(o.evictionPolicy))
	}

	i := &Cache[T]{
		clock:           clock.NewClock(),
		cache:           make(map[string]entry[T]),
		defaultExpiry:   defaultExpiry,
		index:           newEvictionIndex(o.maxEntries, o.evictionPolicy),
		logger:          o.logger,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
	}

	// Asserted rather than assumed: Option cannot name T, so this is where a
	// loader built for another type is caught. Silently leaving the cache
	// without one would be worse than failing — every read would miss, the
	// cache would look merely cold, and the caller would never learn their
	// loader was ignored.
	if o.loader != nil {
		loader, ok := o.loader.(Loader[T])
		if !ok {
			return nil, errors.Wrapf(
				ErrLoaderTypeMismatch, "loader is %T, want memory.Loader[%T]", o.loader, *new(T),
			)
		}

		i.loader = loader
	}

	// Staged, not started: the sweep must not observe a half-built cache, so it
	// is launched at the end of construction once the counters exist.
	//
	// The derived context is what Close cancels, so the sweep stops either on
	// Close or when the caller's own context is done, whichever comes first.
	if o.janitorCtx != nil && o.janitorInterval > 0 {
		janitorCtx, stop := context.WithCancel(o.janitorCtx)
		i.stopJanitor = stop
		i.janitor = func() { go i.sweepEvery(janitorCtx, o.janitorInterval) }
	}

	i.o11y = observability.NewObserver(name, i.logger, i.tracerProvider)

	mp := metrics.EnsureMetricsProvider(i.metricsProvider)

	var err error

	i.cacheHitCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_hits", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache hit counter")
	}

	i.cacheMissCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_misses", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache miss counter")
	}

	i.cacheSetCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_sets", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache set counter")
	}

	i.cacheDelCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_deletes", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache delete counter")
	}

	i.cacheEvictCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_evictions", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache eviction counter")
	}

	i.cacheCapacityEvictCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_capacity_evictions", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache capacity eviction counter")
	}

	// Loads against misses is what says whether the flight is collapsing
	// anything: every miss that had to compute counts once here, and every miss
	// that joined a computation already running counts nowhere but the miss
	// counter.
	i.cacheLoadCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_loads", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache load counter")
	}

	i.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_cache_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache latency histogram")
	}

	// Started last, so the sweep can never observe a partially built cache.
	if i.janitor != nil {
		i.janitor()
	}

	return i, nil
}

// expired reports whether e's deadline has passed. A zero deadline never
// expires.
func (i *Cache[T]) expired(e entry[T]) bool {
	return !e.expiresAt.IsZero() && !i.clock.Now().Before(e.expiresAt)
}

// newEntry stamps value with the deadline resolved from this call's options.
func (i *Cache[T]) newEntry(value *T, opts []cache.WriteOption) entry[T] {
	e := entry[T]{value: value}
	if expiry := cache.EffectiveExpiry(i.defaultExpiry, opts...); expiry > 0 {
		e.expiresAt = i.clock.Now().Add(expiry)
	}

	return e
}

// evictExpired removes key if it is still present and still expired, so a
// concurrent overwrite between the read lock and this write lock is never
// discarded.
//
// This is the only place an entry is dropped for having expired, so it is the
// only place that counts an eviction. An expired entry that a Set or SetMany
// overwrites before any read discovers it is replaced silently and never
// counted: that is a write, not a TTL loss, and folding the two together would
// make the counter useless for the question it exists to answer.
func (i *Cache[T]) evictExpired(ctx context.Context, key string) {
	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	if cur, ok := i.cache[key]; ok && i.expired(cur) {
		delete(i.cache, key)
		i.index.forget(key)
		i.cacheEvictCounter.Add(ctx, 1)
	}
}

// readLocked runs fn holding whichever lock the eviction policy requires.
//
// An LRU bound has to write down the reads it serves, so a cache configured
// that way takes the write lock on its read path and readers stop running
// concurrently with one another. Every other configuration keeps the shared
// read lock, which is why the promotion lives behind evictionIndex.recordRead
// rather than an `if` here: the one method is a no-op exactly when the lock
// held cannot support it.
func (i *Cache[T]) readLocked(fn func()) {
	if i.index.recordsReads() {
		i.cacheMu.Lock()
		defer i.cacheMu.Unlock()
	} else {
		i.cacheMu.RLock()
		defer i.cacheMu.RUnlock()
	}

	fn()
}

// lookup reads one key, recording the read if the policy tracks them.
func (i *Cache[T]) lookup(key string) (entry[T], bool) {
	var (
		e  entry[T]
		ok bool
	)

	i.readLocked(func() {
		if e, ok = i.cache[key]; ok && !i.expired(e) {
			i.index.recordRead(key)
		}
	})

	return e, ok
}

func (i *Cache[T]) Get(ctx context.Context, key string) (*T, error) {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	e, ok := i.lookup(key)

	if ok && !i.expired(e) {
		i.cacheHitCounter.Add(ctx, 1)
		return e.value, nil
	}

	if ok {
		i.evictExpired(ctx, key)
	}

	i.cacheMissCounter.Add(ctx, 1)

	// Counted as a miss before the loader runs, whatever the loader answers: the
	// cache did not hold the key, and a read-through cache that reported its
	// loads as hits could not be told from one that never missed at all.
	if i.loader != nil {
		return i.load(ctx, key)
	}

	return nil, cache.ErrNotFound
}

func (i *Cache[T]) Set(ctx context.Context, key string, value *T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	e := i.newEntry(value, opts)

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	i.cache[key] = e
	i.index.recordWrite(key)
	i.cacheSetCounter.Add(ctx, 1)
	i.evictOverflowLocked(ctx)

	return nil
}

// SetIfPresent overwrites key only if it currently holds a live entry.
//
// Presence is judged under the same lock the write takes, so an entry that
// expires or is deleted concurrently cannot be resurrected: the check and the
// write are one critical section, which is the whole point of the method.
//
// An expired-but-not-yet-swept entry counts as absent. The janitor and the
// read path are both lazy about eviction, so an entry's presence in the map is
// not the same as its being live, and a caller asking "is it still there"
// means the deadline, not the bookkeeping. It is left for the sweeper rather
// than evicted here: this is a write path, and counting a TTL loss discovered
// by a refused write would mix it in with the ones reads discover.
func (i *Cache[T]) SetIfPresent(ctx context.Context, key string, value *T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	e := i.newEntry(value, opts)

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	cur, ok := i.cache[key]
	if !ok || i.expired(cur) {
		i.cacheMissCounter.Add(ctx, 1)

		return cache.ErrNotFound
	}

	i.cache[key] = e
	i.index.recordWrite(key)
	i.cacheSetCounter.Add(ctx, 1)

	// No evictOverflowLocked: this replaced an entry rather than adding one, so
	// the map is exactly the size it was and nothing can have overflowed.

	return nil
}

func (i *Cache[T]) Delete(ctx context.Context, key string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	delete(i.cache, key)
	i.index.forget(key)
	i.cacheDelCounter.Add(ctx, 1)

	return nil
}

func (i *Cache[T]) GetMany(ctx context.Context, keys []string) (map[string]*T, error) {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(keys)))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	var expiredKeys, missingKeys []string

	out := make(map[string]*T, len(keys))

	i.readLocked(func() {
		for _, key := range keys {
			e, ok := i.cache[key]
			if ok && !i.expired(e) {
				out[key] = e.value
				i.index.recordRead(key)
				i.cacheHitCounter.Add(ctx, 1)
				continue
			}

			if ok {
				expiredKeys = append(expiredKeys, key)
			}

			// Collected only when something will read them, so a cache with no
			// loader does not allocate a list of its own misses.
			if i.loader != nil {
				missingKeys = append(missingKeys, key)
			}

			i.cacheMissCounter.Add(ctx, 1)
		}
	})

	for _, key := range expiredKeys {
		i.evictExpired(ctx, key)
	}

	if len(missingKeys) > 0 {
		loaded, err := i.loadMany(ctx, missingKeys)
		if err != nil {
			return nil, err
		}

		maps.Copy(out, loaded)
	}

	return out, nil
}

func (i *Cache[T]) SetMany(ctx context.Context, items map[string]*T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(items)))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	// One deadline for the whole batch: options apply to the call, not per item.
	var expiresAt time.Time
	if expiry := cache.EffectiveExpiry(i.defaultExpiry, opts...); expiry > 0 {
		expiresAt = i.clock.Now().Add(expiry)
	}

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	for key, value := range items {
		i.cache[key] = entry[T]{value: value, expiresAt: expiresAt}
		i.index.recordWrite(key)
		i.cacheSetCounter.Add(ctx, 1)
	}

	// Once for the batch rather than once per item: the overflow is the same
	// either way, and evicting as we go would drop entries this very call is
	// about to add — a batch larger than the bound would otherwise thrash
	// through the whole map instead of settling on its own tail.
	i.evictOverflowLocked(ctx)

	return nil
}

// DeleteMany removes the given keys; keys that are absent are not an error.
func (i *Cache[T]) DeleteMany(ctx context.Context, keys []string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(keys)))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	for _, key := range keys {
		if _, ok := i.cache[key]; ok {
			delete(i.cache, key)
			i.index.forget(key)
			i.cacheDelCounter.Add(ctx, 1)
		}
	}

	return nil
}

// DeleteByPrefix removes every entry whose key begins with prefix. The memory
// provider wholly owns its map, so an empty prefix is permitted and clears
// everything.
func (i *Cache[T]) DeleteByPrefix(ctx context.Context, prefix string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("prefix", prefix))
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	for key := range i.cache {
		if strings.HasPrefix(key, prefix) {
			delete(i.cache, key)
			i.index.forget(key)
			i.cacheDelCounter.Add(ctx, 1)
		}
	}

	return nil
}

// Flush removes every entry. The memory provider wholly owns its store, so no
// namespace is needed.
func (i *Cache[T]) Flush(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, i.clock, i.latencyHist)()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	i.cacheDelCounter.Add(ctx, int64(len(i.cache)))
	i.cache = make(map[string]entry[T])
	i.index.reset()

	return nil
}

func (i *Cache[T]) Ping(ctx context.Context) error {
	_, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Logger().Debug("ping")

	return nil
}

// Close stops the janitor, if one is running. Entries are left in place: the
// map is unreachable once the cache is, and Close is not an eviction.
//
// It is safe to call more than once.
func (i *Cache[T]) Close() error {
	if i.stopJanitor != nil {
		i.stopJanitor()
	}

	return nil
}

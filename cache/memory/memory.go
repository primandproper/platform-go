package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v2/cache"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

const name = "in_memory_cache"

var _ cache.BatchCache[struct{}] = (*inMemoryCacheImpl[struct{}])(nil)

type inMemoryCacheImpl[T any] struct {
	o11y             observability.Observer
	cacheHitCounter  metrics.Int64Counter
	cacheMissCounter metrics.Int64Counter
	cacheSetCounter  metrics.Int64Counter
	cacheDelCounter  metrics.Int64Counter
	latencyHist      metrics.Float64Histogram
	cache            map[string]*T
	cacheMu          sync.RWMutex
}

// NewInMemoryCache builds an in-memory cache.
func NewInMemoryCache[T any](logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (cache.Cache[T], error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	cacheHitCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_cache_hits", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache hit counter")
	}

	cacheMissCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_cache_misses", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache miss counter")
	}

	cacheSetCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_cache_sets", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache set counter")
	}

	cacheDelCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_cache_deletes", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache delete counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_cache_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache latency histogram")
	}

	return &inMemoryCacheImpl[T]{
		o11y:             observability.NewObserver(name, logger, tracerProvider),
		cacheHitCounter:  cacheHitCounter,
		cacheMissCounter: cacheMissCounter,
		cacheSetCounter:  cacheSetCounter,
		cacheDelCounter:  cacheDelCounter,
		latencyHist:      latencyHist,
		cache:            make(map[string]*T),
	}, nil
}

func (i *inMemoryCacheImpl[T]) Get(ctx context.Context, key string) (*T, error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	i.cacheMu.RLock()
	defer i.cacheMu.RUnlock()

	if val, ok := i.cache[key]; ok {
		i.cacheHitCounter.Add(ctx, 1)
		return val, nil
	}

	i.cacheMissCounter.Add(ctx, 1)

	return nil, cache.ErrNotFound
}

func (i *inMemoryCacheImpl[T]) Set(ctx context.Context, key string, value *T) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	i.cache[key] = value
	i.cacheSetCounter.Add(ctx, 1)

	return nil
}

func (i *inMemoryCacheImpl[T]) Delete(ctx context.Context, key string) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	delete(i.cache, key)
	i.cacheDelCounter.Add(ctx, 1)

	return nil
}

func (i *inMemoryCacheImpl[T]) GetMany(ctx context.Context, keys []string) (map[string]*T, error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("length", len(keys))

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	i.cacheMu.RLock()
	defer i.cacheMu.RUnlock()

	out := make(map[string]*T, len(keys))
	for _, key := range keys {
		if val, ok := i.cache[key]; ok {
			out[key] = val
			i.cacheHitCounter.Add(ctx, 1)
			continue
		}

		i.cacheMissCounter.Add(ctx, 1)
	}

	return out, nil
}

func (i *inMemoryCacheImpl[T]) SetMany(ctx context.Context, items map[string]*T) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("length", len(items))

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	i.cacheMu.Lock()
	defer i.cacheMu.Unlock()

	for key, value := range items {
		i.cache[key] = value
		i.cacheSetCounter.Add(ctx, 1)
	}

	return nil
}

func (i *inMemoryCacheImpl[T]) Ping(ctx context.Context) error {
	_, op := i.o11y.Begin(ctx)
	defer op.End()

	op.Logger().Debug("ping")

	return nil
}

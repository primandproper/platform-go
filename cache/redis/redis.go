package redis

import (
	"bytes"
	"context"
	"encoding/gob"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v5/cache"
	"github.com/primandproper/platform-go/v5/cache/redis/slots"
	"github.com/primandproper/platform-go/v5/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v5/circuitbreaking/config"
	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/redis/go-redis/v9"
)

const name = "redis_cache"

// batchSetScript stores every KEYS[i] with the value at ARGV[i+1], applying a
// single millisecond TTL (ARGV[1]) to all of them in one round trip. Vanilla
// MSET cannot attach a TTL, so the writes and their expiry are issued together
// inside the script. A non-positive TTL stores the value without expiry, matching
// go-redis' Set semantics for a zero expiration.
const batchSetScript = `
local ttl = tonumber(ARGV[1])
for i = 1, #KEYS do
    if ttl > 0 then
        redis.call('SET', KEYS[i], ARGV[i + 1], 'PX', ttl)
    else
        redis.call('SET', KEYS[i], ARGV[i + 1])
    end
end
return #KEYS
`

var _ cache.BatchCache[struct{}] = (*redisCacheImpl[struct{}])(nil)

type redisClient interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
	Ping(ctx context.Context) *redis.StatusCmd
}

type redisCacheImpl[T any] struct {
	o11y             observability.Observer
	cacheHitCounter  metrics.Int64Counter
	cacheMissCounter metrics.Int64Counter
	cacheSetCounter  metrics.Int64Counter
	cacheDelCounter  metrics.Int64Counter
	cacheErrCounter  metrics.Int64Counter
	latencyHist      metrics.Float64Histogram
	client           redisClient
	circuitBreaker   circuitbreaking.CircuitBreaker
	expiration       time.Duration
	isCluster        bool
}

// NewRedisCache builds a new redis-backed cache.
func NewRedisCache[T any](cfg *Config, expiration time.Duration, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cb circuitbreaking.CircuitBreaker) (cache.Cache[T], error) {
	if cfg == nil || len(cfg.QueueAddresses) == 0 {
		return nil, fmt.Errorf("at least one redis address is required")
	}

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

	cacheErrCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_cache_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_cache_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache latency histogram")
	}

	return &redisCacheImpl[T]{
		o11y:             observability.NewObserver(name, logger, tracerProvider),
		cacheHitCounter:  cacheHitCounter,
		cacheMissCounter: cacheMissCounter,
		cacheSetCounter:  cacheSetCounter,
		cacheDelCounter:  cacheDelCounter,
		cacheErrCounter:  cacheErrCounter,
		latencyHist:      latencyHist,
		client:           buildRedisClient(cfg),
		circuitBreaker:   circuitbreakingcfg.EnsureCircuitBreaker(cb),
		expiration:       expiration,
		isCluster:        cfg.clusterMode(),
	}, nil
}

func (i *redisCacheImpl[T]) Get(ctx context.Context, key string) (*T, error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	if i.circuitBreaker.CannotProceed() {
		i.cacheMissCounter.Add(ctx, 1)
		return nil, cache.ErrNotFound
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	res, err := i.client.Get(ctx, key).Result()
	if err != nil {
		// A key miss is a healthy response, not an infrastructure failure: don't count it
		// as an error or trip the breaker, and surface the sentinel callers check for.
		if stderrors.Is(err, redis.Nil) {
			i.circuitBreaker.Succeeded()
			i.cacheMissCounter.Add(ctx, 1)
			return nil, cache.ErrNotFound
		}

		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return nil, op.Error(err, "getting from cache")
	}

	x, err := i.decode(res)
	if err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		return nil, err
	}

	if x == nil {
		i.cacheMissCounter.Add(ctx, 1)
		return nil, cache.ErrNotFound
	}

	i.circuitBreaker.Succeeded()
	i.cacheHitCounter.Add(ctx, 1)

	return x, nil
}

func (i *redisCacheImpl[T]) Set(ctx context.Context, key string, value *T) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	if i.circuitBreaker.CannotProceed() {
		return nil
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	encoded, err := i.encode(value)
	if err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		return err
	}

	if setErr := i.client.Set(ctx, key, encoded, i.expiration).Err(); setErr != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return setErr
	}

	i.circuitBreaker.Succeeded()
	i.cacheSetCounter.Add(ctx, 1)

	return nil
}

func (i *redisCacheImpl[T]) Delete(ctx context.Context, key string) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("name", key)

	if i.circuitBreaker.CannotProceed() {
		return nil
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	if err := i.client.Del(ctx, key).Err(); err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return err
	}

	i.circuitBreaker.Succeeded()
	i.cacheDelCounter.Add(ctx, 1)

	return nil
}

func (i *redisCacheImpl[T]) Ping(ctx context.Context) error {
	return i.client.Ping(ctx).Err()
}

// GetMany fetches multiple keys, returning only those that were present. In
// cluster mode MGET requires every key to share a hash slot, so the keys are
// bucketed by slot and fetched one MGET per slot; a single-node client fetches
// them all in one MGET.
func (i *redisCacheImpl[T]) GetMany(ctx context.Context, keys []string) (map[string]*T, error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("length", len(keys))

	out := make(map[string]*T, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	if i.circuitBreaker.CannotProceed() {
		i.cacheMissCounter.Add(ctx, int64(len(keys)))
		return out, nil
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	for _, group := range i.slotGroups(keys) {
		values, err := i.client.MGet(ctx, group...).Result()
		if err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return nil, op.Error(err, "getting many from cache")
		}

		for idx, v := range values {
			s, ok := v.(string)
			if !ok {
				// A nil element (or any non-string) is a missing key.
				i.cacheMissCounter.Add(ctx, 1)
				continue
			}

			decoded, decodeErr := i.decode(s)
			if decodeErr != nil {
				i.cacheErrCounter.Add(ctx, 1)
				return nil, decodeErr
			}

			if decoded == nil {
				i.cacheMissCounter.Add(ctx, 1)
				continue
			}

			out[group[idx]] = decoded
			i.cacheHitCounter.Add(ctx, 1)
		}
	}

	i.circuitBreaker.Succeeded()

	return out, nil
}

// SetMany stores multiple values, each with the cache's configured expiration.
// The writes and their expiry are applied together inside a single Lua script
// (see batchSetScript), which is both atomic and a single round trip. In cluster
// mode EVAL requires every key to share a hash slot, so the batch is split per
// slot.
func (i *redisCacheImpl[T]) SetMany(ctx context.Context, items map[string]*T) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()
	op.Set("length", len(items))

	if len(items) == 0 {
		return nil
	}

	if i.circuitBreaker.CannotProceed() {
		return nil
	}

	startTime := time.Now()
	defer func() {
		i.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	// Encode every value first so a single bad value fails the batch before any
	// write is issued.
	encoded := make(map[string]string, len(items))
	keys := make([]string, 0, len(items))
	for key, value := range items {
		s, err := i.encode(value)
		if err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			return err
		}

		encoded[key] = s
		keys = append(keys, key)
	}

	ttl := i.expiration.Milliseconds()
	for _, group := range i.slotGroups(keys) {
		args := make([]any, 0, len(group)+1)
		args = append(args, ttl)
		for _, key := range group {
			args = append(args, encoded[key])
		}

		if err := i.client.Eval(ctx, batchSetScript, group, args...).Err(); err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return err
		}
	}

	i.circuitBreaker.Succeeded()
	i.cacheSetCounter.Add(ctx, int64(len(keys)))

	return nil
}

// slotGroups splits keys into batches that are safe for a single MGET/EVAL. A
// single-node client has no hash-slot restriction, so all keys go in one group;
// a cluster client requires every key in a call to map to the same slot, so the
// keys are bucketed by slot.
func (i *redisCacheImpl[T]) slotGroups(keys []string) [][]string {
	if !i.isCluster {
		return [][]string{keys}
	}

	return groupBySlot(keys)
}

// groupBySlot buckets keys by their Redis Cluster hash slot, reusing the same
// hashtag-aware slot computation the cluster itself applies.
func groupBySlot(keys []string) [][]string {
	bySlot := make(map[uint16][]string)
	for _, key := range keys {
		slot := slots.SlotForKey(key)
		bySlot[slot] = append(bySlot[slot], key)
	}

	groups := make([][]string, 0, len(bySlot))
	for _, group := range bySlot {
		groups = append(groups, group)
	}

	return groups
}

// encode gob-encodes value into the string form stored in Redis.
func (*redisCacheImpl[T]) encode(value *T) (string, error) {
	var b bytes.Buffer
	if err := gob.NewEncoder(&b).Encode(value); err != nil {
		return "", errors.Wrap(err, "encoding for cache")
	}

	return b.String(), nil
}

// decode reverses encode, gob-decoding a stored string back into a *T.
func (*redisCacheImpl[T]) decode(s string) (*T, error) {
	var x *T
	if err := gob.NewDecoder(strings.NewReader(s)).Decode(&x); err != nil {
		return nil, errors.Wrap(err, "decoding from cache")
	}

	return x, nil
}

// buildRedisClient returns a PublisherProvider for a given address.
func buildRedisClient(cfg *Config) redisClient {
	var c redisClient
	if cfg.clusterMode() {
		c = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        cfg.QueueAddresses,
			Username:     cfg.Username,
			Password:     cfg.Password,
			DialTimeout:  1 * time.Second,
			ReadTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
		})
	} else if len(cfg.QueueAddresses) == 1 {
		c = redis.NewClient(&redis.Options{
			Addr:         cfg.QueueAddresses[0],
			Username:     cfg.Username,
			Password:     cfg.Password,
			DialTimeout:  1 * time.Second,
			ReadTimeout:  1 * time.Second,
			WriteTimeout: 1 * time.Second,
		})
	}

	return c
}

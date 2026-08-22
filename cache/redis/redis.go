package redis

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/redis/slots"
	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/redisclient"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/redis/go-redis/v9"
)

const name = "redis_cache"

// defaultScanPageSize bounds how many keys a single SCAN iteration asks for
// during prefix deletion, when WithScanPageSize is not supplied.
const defaultScanPageSize = 1000

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

var _ cache.Cache[struct{}] = (*Cache[struct{}])(nil)

// ErrCodecTypeMismatch indicates WithCodec was given a codec for a type other
// than the cache's. Option carries no type parameter, so the compiler cannot
// catch this; NewRedisCache reports it instead.
var ErrCodecTypeMismatch = errors.New("codec type does not match cache type")

// scanDelClient is the slice of redisClient that prefix deletion needs; it is
// satisfied by both the cache's own client and the per-master *redis.Client
// handles ForEachMaster yields in cluster mode. redisClient embeds it so the
// two cannot drift.
type scanDelClient interface {
	Scan(ctx context.Context, cursor uint64, match string, count int64) *redis.ScanCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

type redisClient interface {
	scanDelClient

	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	SetArgs(ctx context.Context, key string, value any, a redis.SetArgs) *redis.StatusCmd
	MGet(ctx context.Context, keys ...string) *redis.SliceCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	Ping(ctx context.Context) *redis.StatusCmd
	Close() error
}

// Cache is the redis-backed cache.Cache implementation. It is exported, and
// returned by NewRedisCache, so a caller can depend on this cache rather than
// on the interface every provider shares — and so face only the failures this
// provider actually has, rather than the union of every provider's.
type Cache[T any] struct {
	o11y             observability.Observer
	logger           logging.Logger
	tracerProvider   tracing.Provider
	metricsProvider  metrics.Provider
	codec            cache.Codec[T]
	cacheHitCounter  metrics.Int64Counter
	cacheMissCounter metrics.Int64Counter
	cacheSetCounter  metrics.Int64Counter
	cacheDelCounter  metrics.Int64Counter
	cacheErrCounter  metrics.Int64Counter
	latencyHist      metrics.Float64Histogram
	client           redisClient
	circuitBreaker   circuitbreaking.CircuitBreaker
	namespace        string
	expiration       time.Duration
	scanPageSize     int64
	isCluster        bool
}

// NewRedisCache builds a new redis-backed cache. When cfg.Namespace is set,
// every key is transparently prefixed with it: callers always use bare keys,
// the namespace marks which entries this cache owns, and Flush becomes
// possible (it deletes exactly the namespace's keys). Without a namespace,
// Flush and an empty-prefix DeleteByPrefix return cache.ErrNamespaceRequired
// rather than guess at ownership in a possibly shared database.
//
// Values are stored through cache.NewDefaultCodec unless WithCodec says
// otherwise.
// Entries carry no record of the codec that wrote them, so pointing a cache
// with one codec at a store warmed by another produces decode errors until the
// old entries expire; give the new codec its own cfg.Namespace when switching.
func NewRedisCache[T any](cfg *Config, expiration time.Duration, cb circuitbreaking.CircuitBreaker, opts ...Option) (*Cache[T], error) {
	if cfg == nil || len(cfg.Addresses) == 0 {
		return nil, fmt.Errorf("at least one redis address is required")
	}

	o := &options{scanPageSize: defaultScanPageSize}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	impl := &Cache[T]{
		codec:           cache.NewDefaultCodec[T](),
		circuitBreaker:  circuitbreakingcfg.EnsureCircuitBreaker(cb, circuitbreakingcfg.WithLogger(o.logger)),
		namespace:       cfg.Namespace,
		expiration:      expiration,
		scanPageSize:    o.scanPageSize,
		isCluster:       cfg.clusterMode(),
		logger:          o.logger,
		tracerProvider:  o.tracerProvider,
		metricsProvider: o.metricsProvider,
	}

	// Asserted rather than assumed: Option cannot name T, so this is where a
	// codec built for another type is caught. Silently keeping the default
	// would be worse than failing — the cache would encode correctly and the
	// caller would never learn their codec was ignored.
	if o.codec != nil {
		codec, ok := o.codec.(cache.Codec[T])
		if !ok {
			return nil, errors.Wrapf(
				ErrCodecTypeMismatch, "codec is %T, want cache.Codec[%T]", o.codec, *new(T),
			)
		}

		impl.codec = codec
	}

	impl.o11y = observability.NewObserver(name, impl.logger, impl.tracerProvider)

	mp := metrics.EnsureMetricsProvider(impl.metricsProvider)

	var err error

	impl.cacheHitCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_hits", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache hit counter")
	}

	impl.cacheMissCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_misses", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache miss counter")
	}

	impl.cacheSetCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_sets", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache set counter")
	}

	impl.cacheDelCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_deletes", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache delete counter")
	}

	impl.cacheErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_cache_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache error counter")
	}

	impl.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_cache_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating cache latency histogram")
	}

	// Built last, so a failed constructor never leaves a connected client behind.
	if impl.client, err = buildRedisClient(cfg); err != nil {
		return nil, err
	}

	return impl, nil
}

// key returns the stored form of a caller key: the configured namespace
// prepended. Every operation goes through this, so callers only ever see bare
// keys.
func (i *Cache[T]) key(k string) string {
	return i.namespace + k
}

func (i *Cache[T]) Get(ctx context.Context, key string) (*T, error) {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	if i.circuitBreaker.CannotProceed() {
		return nil, cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	res, err := i.client.Get(ctx, i.key(key)).Result()
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
		return nil, op.Error(err, "decoding cached value")
	}

	if x == nil {
		i.cacheMissCounter.Add(ctx, 1)
		return nil, cache.ErrNotFound
	}

	i.circuitBreaker.Succeeded()
	i.cacheHitCounter.Add(ctx, 1)

	return x, nil
}

func (i *Cache[T]) Set(ctx context.Context, key string, value *T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	encoded, err := i.encode(value)
	if err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		return op.Error(err, "encoding value for cache")
	}

	if setErr := i.client.Set(ctx, i.key(key), encoded, cache.EffectiveExpiry(i.expiration, opts...)).Err(); setErr != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		// Every read path here reports through op.Error; the writes returned bare,
		// so a trace showed a red span for a cache miss and a green one for a
		// write that never reached redis.
		return op.Error(setErr, "setting cache value")
	}

	i.circuitBreaker.Succeeded()
	i.cacheSetCounter.Add(ctx, 1)

	return nil
}

// SetIfPresent writes key only if redis already holds it, as a single SET with
// the XX flag.
//
// This is the reason the method is on the interface at all: redis decides the
// condition and performs the write in one command, so nothing can delete the
// key in between. A GET followed by a SET would leave exactly that window, and
// no amount of care on this side closes it.
//
// A refusal comes back as redis.Nil — the same reply a missing GET produces —
// and is translated to ErrNotFound. Like a read miss it is a healthy answer
// rather than an infrastructure failure, so it feeds the breaker a success: the
// server responded, correctly, that the condition did not hold.
func (i *Cache[T]) SetIfPresent(ctx context.Context, key string, value *T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	encoded, err := i.encode(value)
	if err != nil {
		i.cacheErrCounter.Add(ctx, 1)

		return op.Error(err, "encoding value for cache")
	}

	// TTL carries the resolved expiry, and zero means "no expiry" to go-redis
	// exactly as it does to Set — so an entry written here keeps whatever expiry
	// policy the caller's options describe rather than inheriting the one the
	// previous write happened to use. KeepTTL is deliberately not set: this
	// method's callers are refreshing a deadline, not preserving one.
	setErr := i.client.SetArgs(ctx, i.key(key), encoded, redis.SetArgs{
		Mode: "XX",
		TTL:  cache.EffectiveExpiry(i.expiration, opts...),
	}).Err()

	switch {
	case stderrors.Is(setErr, redis.Nil):
		i.circuitBreaker.Succeeded()
		i.cacheMissCounter.Add(ctx, 1)

		return cache.ErrNotFound
	case setErr != nil:
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()

		return op.Error(setErr, "setting cache value if present")
	}

	i.circuitBreaker.Succeeded()
	i.cacheSetCounter.Add(ctx, 1)

	return nil
}

func (i *Cache[T]) Delete(ctx context.Context, key string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("name", key))
	defer op.End()

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	if err := i.client.Del(ctx, i.key(key)).Err(); err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "deleting from cache")
	}

	i.circuitBreaker.Succeeded()
	i.cacheDelCounter.Add(ctx, 1)

	return nil
}

// DeleteMany removes the given keys. In cluster mode a multi-key DEL requires
// every key to share a hash slot, so the keys are bucketed by slot and
// deleted one DEL per slot; a single-node client deletes them in one DEL.
func (i *Cache[T]) DeleteMany(ctx context.Context, keys []string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(keys)))
	defer op.End()

	if len(keys) == 0 {
		return nil
	}

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	stored := make([]string, len(keys))
	for idx, k := range keys {
		stored[idx] = i.key(k)
	}

	for _, group := range i.slotGroups(stored) {
		if len(group) == 0 {
			continue
		}

		deleted, err := i.client.Del(ctx, group...).Result()
		if err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return op.Error(err, "deleting many from cache")
		}

		i.cacheDelCounter.Add(ctx, deleted)
	}

	i.circuitBreaker.Succeeded()

	return nil
}

// DeleteByPrefix removes every entry whose (caller-visible) key begins with
// prefix, via a cursor SCAN over the namespaced pattern. Without a configured
// namespace an empty prefix is refused with cache.ErrNamespaceRequired —
// matching every key in a possibly shared database is not ownership.
func (i *Cache[T]) DeleteByPrefix(ctx context.Context, prefix string) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("prefix", prefix))
	defer op.End()

	if i.namespace == "" && prefix == "" {
		return cache.ErrNamespaceRequired
	}

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	pattern := escapeGlob(i.key(prefix)) + "*"

	deleted, err := i.deleteByPattern(ctx, pattern)
	i.cacheDelCounter.Add(ctx, deleted)
	if err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()
		return op.Error(err, "deleting by prefix from cache")
	}

	i.circuitBreaker.Succeeded()

	return nil
}

// Flush removes every entry this cache owns. Ownership is the configured
// namespace; without one this cache cannot distinguish its entries in a
// possibly shared database, and Flush returns cache.ErrNamespaceRequired
// rather than reach for FLUSHDB.
func (i *Cache[T]) Flush(ctx context.Context) error {
	if i.namespace == "" {
		return cache.ErrNamespaceRequired
	}

	return i.DeleteByPrefix(ctx, "")
}

// deleteByPattern scans for pattern and deletes what it finds. SCAN is
// per-node, so in cluster mode every master is scanned; on a single node the
// cache's own client is scanned directly.
func (i *Cache[T]) deleteByPattern(ctx context.Context, pattern string) (int64, error) {
	if clusterClient, ok := i.client.(*redis.ClusterClient); ok && i.isCluster {
		var total int64
		err := clusterClient.ForEachMaster(ctx, func(ctx context.Context, master *redis.Client) error {
			n, scanErr := i.scanAndDelete(ctx, master, pattern)
			total += n
			return scanErr
		})

		return total, err
	}

	return i.scanAndDelete(ctx, i.client, pattern)
}

// scanAndDelete drives one client's cursor SCAN over pattern, deleting each
// page. Pages are slot-grouped before DEL so cluster masters never see a
// cross-slot multi-key command.
func (i *Cache[T]) scanAndDelete(ctx context.Context, c scanDelClient, pattern string) (int64, error) {
	var (
		deleted int64
		cursor  uint64
	)

	for {
		keys, next, err := c.Scan(ctx, cursor, pattern, i.scanPageSize).Result()
		if err != nil {
			return deleted, errors.Wrap(err, "scanning for keys")
		}

		for _, group := range i.slotGroups(keys) {
			if len(group) == 0 {
				continue
			}

			n, delErr := c.Del(ctx, group...).Result()
			deleted += n
			if delErr != nil {
				return deleted, errors.Wrap(delErr, "deleting scanned keys")
			}
		}

		cursor = next
		if cursor == 0 {
			return deleted, nil
		}
	}
}

// Ping reports whether redis is reachable.
//
// It goes through the observer and the breaker like every other method. It used
// to bypass both, which made the one call whose entire purpose is to report
// reachability the one call that neither recorded a failure nor let the breaker
// learn from it — and left a health check hitting a dead redis emitting nothing.
//
// A refusal from an open breaker is ErrUnavailable rather than a redis error:
// the breaker is open precisely because redis has been failing, and answering
// "unavailable" without waiting for another timeout is the point of having one.
func (i *Cache[T]) Ping(ctx context.Context) error {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	if i.circuitBreaker.CannotProceed() {
		return op.Error(cache.ErrUnavailable, "pinging cache")
	}

	if err := i.client.Ping(ctx).Err(); err != nil {
		i.cacheErrCounter.Add(ctx, 1)
		i.circuitBreaker.Failed()

		return op.Error(err, "pinging cache")
	}

	i.circuitBreaker.Succeeded()

	return nil
}

// GetMany fetches multiple keys, returning only those that were present. In
// cluster mode MGET requires every key to share a hash slot, so the keys are
// bucketed by slot and fetched one MGET per slot; a single-node client fetches
// them all in one MGET. Results are keyed by the caller's bare keys.
func (i *Cache[T]) GetMany(ctx context.Context, keys []string) (map[string]*T, error) {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(keys)))
	defer op.End()

	out := make(map[string]*T, len(keys))
	if len(keys) == 0 {
		return out, nil
	}

	if i.circuitBreaker.CannotProceed() {
		return nil, cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	stored := make([]string, len(keys))
	callerKey := make(map[string]string, len(keys))
	for idx, k := range keys {
		sk := i.key(k)
		stored[idx] = sk
		callerKey[sk] = k
	}

	for _, group := range i.slotGroups(stored) {
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
				return nil, op.Error(decodeErr, "decoding cached value")
			}

			if decoded == nil {
				i.cacheMissCounter.Add(ctx, 1)
				continue
			}

			out[callerKey[group[idx]]] = decoded
			i.cacheHitCounter.Add(ctx, 1)
		}
	}

	i.circuitBreaker.Succeeded()

	return out, nil
}

// SetMany stores multiple values, each with the expiration resolved from this
// call's options (the cache's configured default when none are given). The
// writes and their expiry are applied together inside a single Lua script
// (see batchSetScript), which is both atomic and a single round trip. In cluster
// mode EVAL requires every key to share a hash slot, so the batch is split per
// slot.
func (i *Cache[T]) SetMany(ctx context.Context, items map[string]*T, opts ...cache.WriteOption) error {
	ctx, op := i.o11y.Begin(ctx, observability.WithValue("length", len(items)))
	defer op.End()

	if len(items) == 0 {
		return nil
	}

	if i.circuitBreaker.CannotProceed() {
		return cache.ErrUnavailable
	}

	defer op.Time(ctx, nil, i.latencyHist)()

	// Encode every value first so a single bad value fails the batch before any
	// write is issued.
	encoded := make(map[string]string, len(items))
	stored := make([]string, 0, len(items))
	for key, value := range items {
		s, err := i.encode(value)
		if err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			return op.Error(err, "encoding value for cache")
		}

		sk := i.key(key)
		encoded[sk] = s
		stored = append(stored, sk)
	}

	expiry := cache.EffectiveExpiry(i.expiration, opts...).Milliseconds()
	for _, group := range i.slotGroups(stored) {
		args := make([]any, 0, len(group)+1)
		args = append(args, expiry)
		for _, key := range group {
			args = append(args, encoded[key])
		}

		if err := i.client.Eval(ctx, batchSetScript, group, args...).Err(); err != nil {
			i.cacheErrCounter.Add(ctx, 1)
			i.circuitBreaker.Failed()
			return op.Error(err, "setting many cache values")
		}
	}

	i.circuitBreaker.Succeeded()
	i.cacheSetCounter.Add(ctx, int64(len(stored)))

	return nil
}

// slotGroups splits keys into batches that are safe for a single
// MGET/EVAL/DEL. A single-node client has no hash-slot restriction, so all
// keys go in one group; a cluster client requires every key in a call to map
// to the same slot, so the keys are bucketed by slot.
func (i *Cache[T]) slotGroups(keys []string) [][]string {
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

// escapeGlob backslash-escapes SCAN MATCH glob metacharacters so a literal
// prefix containing *, ?, [, ], or \ matches itself rather than acting as a
// pattern.
func escapeGlob(s string) string {
	if !strings.ContainsAny(s, `*?[]\`) {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '*', '?', '[', ']', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}

	return b.String()
}

// encode runs the configured codec, yielding the string form stored in Redis.
func (i *Cache[T]) encode(value *T) (string, error) {
	b, err := i.codec.Encode(value)
	if err != nil {
		return "", errors.Wrap(err, "encoding for cache")
	}

	return string(b), nil
}

// decode reverses encode through the configured codec.
func (i *Cache[T]) decode(s string) (*T, error) {
	x, err := i.codec.Decode([]byte(s))
	if err != nil {
		return nil, errors.Wrap(err, "decoding from cache")
	}

	return x, nil
}

// buildRedisClient opens the connection this cache reads and writes through.
func buildRedisClient(cfg *Config) (redisClient, error) {
	return redisclient.New(redisclient.Config{
		Username:  cfg.Username,
		Password:  cfg.Password,
		Addresses: cfg.Addresses,
		Cluster:   cfg.clusterMode(),
	})
}

// Close releases the connection pool. It does not evict anything: the entries
// live in redis and outlive any one client.
//
// It is safe to call more than once — go-redis's Close is idempotent.
func (c *Cache[T]) Close() error {
	if c.client == nil {
		return nil
	}

	return c.client.Close()
}

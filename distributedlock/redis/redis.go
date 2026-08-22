package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/internal/redisclient"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/redis/go-redis/v9"
)

const serviceName = "redis_distributed_lock"

// redisKeyKey names the prefixed key this provider actually writes, which is not
// the same datum as the lock's name and so does not belong under
// keys.LockKeyKey. The lock's name is what correlates a trace across providers;
// this is what an operator types into redis-cli.
const redisKeyKey = "lock.redis_key"

// releaseScript atomically deletes the lock key only if its current value matches
// the supplied ownership token. Returns 1 on successful release, 0 if the caller
// no longer owns the lock (expired, stolen, already released).
const releaseScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
else
    return 0
end
`

// refreshScript atomically extends the lock's TTL only if its current value matches
// the supplied ownership token. Returns 1 on successful refresh, 0 if the caller
// no longer owns the lock.
const refreshScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("PEXPIRE", KEYS[1], ARGV[2])
else
    return 0
end
`

// redisClient is the subset of go-redis we depend on. Defining it as an interface
// keeps the Locker testable without requiring a real Redis for unit tests.
type redisClient interface {
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	Ping(ctx context.Context) *redis.StatusCmd
	Close() error
}

var (
	_ distributedlock.Locker = (*Locker)(nil)
	_ distributedlock.Lock   = (*lock)(nil)
)

type Locker struct {
	o11y           observability.Observer
	client         redisClient
	circuitBreaker circuitbreaking.CircuitBreaker
	acquireCounter metrics.Int64Counter
	releaseCounter metrics.Int64Counter
	refreshCounter metrics.Int64Counter
	contendCounter metrics.Int64Counter
	errCounter     metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	keyPrefix      string
}

// NewRedisLocker constructs a new Redis-backed distributedlock.Locker.
func NewRedisLocker(
	cfg *Config,
	cb circuitbreaking.CircuitBreaker,
	opts ...Option,
) (*Locker, error) {
	if cfg == nil {
		return nil, distributedlock.ErrNilConfig
	}

	client, err := buildRedisClient(cfg)
	if err != nil {
		return nil, platformerrors.Wrap(err, "building redis client")
	}

	o := newOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	acquireCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_acquires", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating acquire counter")
	}
	releaseCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_releases", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating release counter")
	}
	refreshCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_refreshes", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating refresh counter")
	}
	contendCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_contended", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating contention counter")
	}
	errCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return &Locker{
		o11y:           observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		client:         client,
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb, circuitbreakingcfg.WithLogger(o.logger)),
		acquireCounter: acquireCounter,
		releaseCounter: releaseCounter,
		refreshCounter: refreshCounter,
		contendCounter: contendCounter,
		errCounter:     errCounter,
		latencyHist:    latencyHist,
		keyPrefix:      cfg.KeyPrefix,
	}, nil
}

// Acquire implements distributedlock.Locker.
func (l *Locker) Acquire(ctx context.Context, key string, ttl time.Duration) (distributedlock.Lock, error) {
	ctx, op := l.o11y.Begin(ctx,
		observability.WithValue(keys.LockKeyKey, key),
		observability.WithValue(keys.LockTTLKey, ttl),
	)
	defer op.End()

	if key == "" {
		return nil, distributedlock.ErrEmptyKey
	}
	if ttl <= 0 {
		return nil, distributedlock.ErrInvalidTTL
	}
	if l.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	defer op.Time(ctx, nil, l.latencyHist)()

	token := identifiers.New()
	fullKey := l.keyPrefix + key
	ok, err := l.client.SetNX(ctx, fullKey, token, ttl).Result()
	if err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return nil, op.Error(err, "acquiring lock %q", key)
	}
	if !ok {
		// Backend healthy, contention is the expected outcome — don't fail the breaker.
		l.contendCounter.Add(ctx, 1)
		l.circuitBreaker.Succeeded()
		return nil, distributedlock.ErrLockNotAcquired
	}

	l.acquireCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()

	return &lock{
		locker:  l,
		key:     key,
		fullKey: fullKey,
		token:   token,
		ttl:     ttl,
	}, nil
}

// Ping implements distributedlock.Locker.
func (l *Locker) Ping(ctx context.Context) error {
	return l.client.Ping(ctx).Err()
}

// Close implements distributedlock.Locker.
func (l *Locker) Close() error {
	return l.client.Close()
}

// release runs the compare-and-delete release script and translates the result.
func (l *Locker) release(ctx context.Context, key, fullKey, token string) error {
	ctx, op := l.o11y.Begin(ctx,
		observability.WithValue(keys.LockKeyKey, key),
		observability.WithValue(redisKeyKey, fullKey),
	)
	defer op.End()

	if l.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	defer op.Time(ctx, nil, l.latencyHist)()

	res, err := l.client.Eval(ctx, releaseScript, []string{fullKey}, token).Int64()
	if err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return op.Error(err, "releasing lock")
	}
	if res == 0 {
		return distributedlock.ErrLockNotHeld
	}

	l.releaseCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()
	return nil
}

// refresh runs the compare-and-pexpire refresh script and translates the result.
func (l *Locker) refresh(ctx context.Context, key, fullKey, token string, ttl time.Duration) error {
	ctx, op := l.o11y.Begin(ctx,
		observability.WithValue(keys.LockKeyKey, key),
		observability.WithValue(redisKeyKey, fullKey),
		observability.WithValue(keys.LockTTLKey, ttl),
	)
	defer op.End()

	if ttl <= 0 {
		return distributedlock.ErrInvalidTTL
	}
	if l.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	defer op.Time(ctx, nil, l.latencyHist)()

	res, err := l.client.Eval(ctx, refreshScript, []string{fullKey}, token, ttl.Milliseconds()).Int64()
	if err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return op.Error(err, "refreshing lock")
	}
	if res == 0 {
		return distributedlock.ErrLockNotHeld
	}

	l.refreshCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()
	return nil
}

// lock is the redis-backed Lock handle.
type lock struct {
	locker  *Locker
	key     string
	fullKey string
	token   string
	ttl     time.Duration
}

// Key implements distributedlock.Lock.
func (l *lock) Key() string { return l.key }

// TTL implements distributedlock.Lock.
func (l *lock) TTL() time.Duration { return l.ttl }

// Release implements distributedlock.Lock.
func (l *lock) Release(ctx context.Context) error {
	return l.locker.release(ctx, l.key, l.fullKey, l.token)
}

// Refresh implements distributedlock.Lock.
func (l *lock) Refresh(ctx context.Context, ttl time.Duration) error {
	if err := l.locker.refresh(ctx, l.key, l.fullKey, l.token, ttl); err != nil {
		return err
	}
	l.ttl = ttl
	return nil
}

// buildRedisClient opens the connection this locker acquires and refreshes
// through.
func buildRedisClient(cfg *Config) (redisClient, error) {
	return redisclient.New(redisclient.Config{
		Username:  cfg.Username,
		Password:  cfg.Password,
		Addresses: cfg.Addresses,
	})
}

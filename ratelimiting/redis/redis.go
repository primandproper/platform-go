package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/identifiers"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/ratelimiting"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/redis/go-redis/v9"
)

// Config configures a Redis-backed rate limiter.
type Config struct {
	Username  string   `env:"USERNAME"  json:"username"`
	Password  string   `env:"PASSWORD"  json:"password,omitempty"`
	Addresses []string `env:"ADDRESSES" json:"addresses"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Addresses, validation.Required, validation.Length(1, 0)),
	)
}

// slidingWindowScript atomically checks and increments a sliding window counter.
// Returns 1 if the request is allowed, 0 if rate limited.
const slidingWindowScript = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local member = ARGV[4]
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window_ms)
local count = redis.call('ZCARD', key)
if count < limit then
    redis.call('ZADD', key, now, member)
    redis.call('PEXPIRE', key, window_ms * 2)
    return 1
end
return 0
`

type redisClient interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	Close() error
}

var _ ratelimiting.RateLimiter = (*rateLimiter)(nil)

const redisName = "redis_rate_limiter"

type rateLimiter struct {
	client          redisClient
	allowedCounter  metrics.Int64Counter
	rejectedCounter metrics.Int64Counter
	errorCounter    metrics.Int64Counter
	requestsPerSec  float64
}

// NewRedisRateLimiter returns a RateLimiter backed by Redis using a sliding window algorithm.
func NewRedisRateLimiter(cfg Config, metricsProvider metrics.Provider, requestsPerSec float64) (ratelimiting.RateLimiter, error) {
	var client redisClient
	if len(cfg.Addresses) > 1 {
		client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    cfg.Addresses,
			Username: cfg.Username,
			Password: cfg.Password,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr:     cfg.Addresses[0],
			Username: cfg.Username,
			Password: cfg.Password,
		})
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	allowedCounter, err := mp.NewInt64Counter(redisName + "_allowed")
	if err != nil {
		return nil, errors.Wrap(err, "creating allowed counter")
	}

	rejectedCounter, err := mp.NewInt64Counter(redisName + "_rejected")
	if err != nil {
		return nil, errors.Wrap(err, "creating rejected counter")
	}

	errorCounter, err := mp.NewInt64Counter(redisName + "_errors")
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	return &rateLimiter{
		client:          client,
		requestsPerSec:  requestsPerSec,
		allowedCounter:  allowedCounter,
		rejectedCounter: rejectedCounter,
		errorCounter:    errorCounter,
	}, nil
}

// Allow returns true if the key is within the rate limit.
func (r *rateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	now := time.Now().UnixMilli()
	windowMS := int64(1000)
	// The member must be unique per request: ZADD on a duplicate member only
	// updates its score, so keying solely on the millisecond timestamp would
	// collapse every request within the same millisecond into a single ZSET
	// entry and let the limit be bypassed under load. The score stays `now` so
	// ZREMRANGEBYSCORE still evicts by window.
	member := fmt.Sprintf("%d-%s", now, identifiers.New())

	result, err := r.client.Eval(ctx, slidingWindowScript,
		[]string{fmt.Sprintf("ratelimit:%s", key)},
		now,
		windowMS,
		int64(r.requestsPerSec),
		member,
	).Int64()
	if err != nil {
		r.errorCounter.Add(ctx, 1)
		return false, err
	}

	allowed := result == 1
	if allowed {
		r.allowedCounter.Add(ctx, 1)
	} else {
		r.rejectedCounter.Add(ctx, 1)
	}
	return allowed, nil
}

// Close closes the underlying Redis client.
func (r *rateLimiter) Close() error {
	return r.client.Close()
}

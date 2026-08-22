package redis

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/internal/redisclient"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/redis/go-redis/v9"
)

// Config configures a Redis-backed rate limiter.
type Config struct {
	Username  string   `env:"USERNAME"  json:"username,omitempty"  yaml:"username,omitempty"`
	Password  string   `env:"PASSWORD"  json:"password,omitempty"  yaml:"password,omitempty"`
	Addresses []string `env:"ADDRESSES" json:"addresses,omitempty" yaml:"addresses,omitempty"`
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

// retryAfterScript reports how many milliseconds a saturated window needs
// before its oldest entry falls out of it, or 0 when the window has room now.
//
// It is read-only, unlike slidingWindowScript: it uses an exclusive-minimum
// ZCOUNT to ignore the entries that have aged out rather than deleting them,
// so consulting the hint cannot change what the next Allow decides. The
// exclusive bound matches slidingWindowScript's ZREMRANGEBYSCORE, which evicts
// scores up to and including the floor.
const retryAfterScript = `
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local floor = string.format("(%d", now - window_ms)
if redis.call('ZCOUNT', key, floor, '+inf') < limit then
    return 0
end
local oldest = redis.call('ZRANGEBYSCORE', key, floor, '+inf', 'LIMIT', 0, 1, 'WITHSCORES')
if #oldest < 2 then
    return 0
end
local wait = tonumber(oldest[2]) + window_ms - now
if wait < 0 then
    return 0
end
return wait
`

type redisClient interface {
	Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	Close() error
}

var (
	_ ratelimiting.RateLimiter = (*RateLimiter)(nil)
	_ ratelimiting.RetryHinter = (*RateLimiter)(nil)
)

const redisName = "redis_rate_limiter"

var _ ratelimiting.RateLimiter = (*RateLimiter)(nil)

// RateLimiter is the Redis ratelimiting.RateLimiter implementation. It is
// exported, and returned by NewRedisRateLimiter, so a caller who has chosen
// Redis can depend on that choice rather than on the interface every limiter
// shares.
type RateLimiter struct {
	o11y            observability.Observer
	client          redisClient
	allowedCounter  metrics.Int64Counter
	rejectedCounter metrics.Int64Counter
	errorCounter    metrics.Int64Counter
	requestsPerSec  float64
	burstSize       int
}

// NewRedisRateLimiter returns a RateLimiter backed by Redis using a sliding window algorithm.
//
// It takes a context so the config goes through its own ValidateWithContext
// rather than through a restatement of one of its rules: the address check here
// used to be spelled out by hand, which is how it came to be the only rule of
// the three that ran.
func NewRedisRateLimiter(ctx context.Context, cfg Config, requestsPerSec float64, burstSize int, opts ...Option) (*RateLimiter, error) {
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating redis rate limiter config")
	}

	client, err := redisclient.New(redisclient.Config{
		Username:  cfg.Username,
		Password:  cfg.Password,
		Addresses: cfg.Addresses,
	})
	if err != nil {
		return nil, errors.Wrap(err, "building redis client")
	}

	o := newOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

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

	return &RateLimiter{
		o11y:            observability.NewObserver(redisName, o.logger, o.tracerProvider),
		client:          client,
		requestsPerSec:  requestsPerSec,
		burstSize:       burstSize,
		allowedCounter:  allowedCounter,
		rejectedCounter: rejectedCounter,
		errorCounter:    errorCounter,
	}, nil
}

// Allow returns true if the key is within the rate limit.
func (r *RateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	now := time.Now().UnixMilli()
	limit, windowMS := r.window()

	// The member must be unique per request: ZADD on a duplicate member only
	// updates its score, so keying solely on the millisecond timestamp would
	// collapse every request within the same millisecond into a single ZSET
	// entry and let the limit be bypassed under load. The score stays `now` so
	// ZREMRANGEBYSCORE still evicts by window.
	member := fmt.Sprintf("%d-%s", now, identifiers.New())

	result, err := r.client.Eval(ctx, slidingWindowScript,
		[]string{redisKey(key)},
		now,
		windowMS,
		limit,
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

// RetryAfter reports when the oldest request in key's window falls out of it,
// which is the earliest moment a saturated window has room again.
//
// It costs a round trip, so it is worth making only on the refusal path — which
// is where ratelimiting.RetryAfterFor calls it. A window with room reports no
// hint rather than zero: there is nothing to wait for, and a Retry-After of 0
// invites the client back immediately for a token it was never refused.
//
// The estimate can be beaten. Nothing reserves the slot it describes, so a
// caller that waits exactly this long may find another has taken it — which is
// why RetryHinter documents the answer as a floor.
func (r *RateLimiter) RetryAfter(ctx context.Context, key string) (time.Duration, bool) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	limit, windowMS := r.window()

	waitMS, err := r.client.Eval(ctx, retryAfterScript,
		[]string{redisKey(key)},
		time.Now().UnixMilli(),
		windowMS,
		limit,
	).Int64()
	if err != nil {
		// Counted and acknowledged, not returned: the caller is already
		// refusing this request and a missing hint only costs it a header.
		r.errorCounter.Add(ctx, 1)
		op.Acknowledge(err, "estimating retry delay")

		return 0, false
	}

	if waitMS <= 0 {
		return 0, false
	}

	return time.Duration(waitMS) * time.Millisecond, true
}

// window maps the token-bucket-style (requestsPerSec, burstSize) config onto a
// sliding window: allow up to `limit` requests per window, where the window is
// the time to accrue a full burst at the steady rate (burst / rate seconds).
// This honors BurstSize and avoids the old int64(requestsPerSec) truncation
// that floored any sub-1 rate to a limit of 0 (rejecting everything).
//
// Allow and RetryAfter both read it, so the window the hint measures against is
// always the window the decision was made in.
func (r *RateLimiter) window() (limit, windowMS int64) {
	limit = max(int64(r.burstSize), 1)

	perSec := r.requestsPerSec
	if perSec <= 0 {
		perSec = 1
	}

	return limit, max(int64(math.Ceil(float64(limit)/perSec*1000)), 1)
}

// redisKey namespaces a caller's key inside Redis.
func redisKey(key string) string {
	return fmt.Sprintf("ratelimit:%s", key)
}

// Close closes the underlying Redis client.
func (r *RateLimiter) Close() error {
	return r.client.Close()
}

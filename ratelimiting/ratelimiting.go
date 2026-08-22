package ratelimiting

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"golang.org/x/time/rate"
)

const inMemoryName = "in_memory_rate_limiter"

// ErrRateLimited reports that a limiter refused an operation. Allow expresses a
// refusal as (false, nil) because the caller is usually deciding what to do
// next; this sentinel exists for the callers that have to hand the refusal back
// as an error instead — an http.RoundTripper, for one, has nowhere else to put
// it. Callers branch on it with errors.Is rather than on a bare false.
var ErrRateLimited = errors.New("rate limited")

// RateLimiter limits the rate of operations per key.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
	Close() error
}

var (
	_ RateLimiter = (*InMemoryRateLimiter)(nil)
	_ RetryHinter = (*InMemoryRateLimiter)(nil)
)

// InMemoryRateLimiter is the process-local RateLimiter, backed by a token
// bucket per key. It is exported, and returned by NewInMemoryRateLimiter, so a
// caller who has chosen it can depend on that choice rather than on the
// interface every limiter shares.
type InMemoryRateLimiter struct {
	o11y                   observability.Observer
	clock                  clock.Clock
	allowedCounter         metrics.Int64Counter
	rejectedCounter        metrics.Int64Counter
	idleEvictedCounter     metrics.Int64Counter
	capacityEvictedCounter metrics.Int64Counter
	limitersGauge          metrics.Int64Gauge
	stop                   chan struct{}
	done                   chan struct{}
	limiters               sync.Map
	// tracked is how many keys limiters holds. sync.Map cannot be asked its
	// size, and the bound has to be checked on the insert that crosses it
	// rather than at the next sweep — a burst of distinct keys inside one
	// window is exactly the case the bound exists for.
	tracked        atomic.Int64
	requestsPerSec float64
	burstSize      int
	maxLimiters    int
	idleTTL        time.Duration
	sweepInterval  time.Duration
	stopOnce       sync.Once
	// evicting admits one capacity eviction at a time. A pass is a full scan,
	// and a caller that finds one already running has nothing to add by
	// starting a second: the running pass will free the same slots.
	evicting sync.Mutex
}

// NewInMemoryRateLimiter returns a RateLimiter that uses per-key limiters in
// memory.
//
// The returned limiter owns a goroutine that reclaims the limiters of keys that
// have stopped arriving, so Close is not optional: a limiter that is never
// closed keeps that goroutine, and itself, alive for the life of the process.
// See the package documentation for what is retained and for how long.
func NewInMemoryRateLimiter(requestsPerSec float64, burstSize int, opts ...Option) (*InMemoryRateLimiter, error) {
	o := newOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	allowedCounter, err := mp.NewInt64Counter(inMemoryName + "_allowed")
	if err != nil {
		return nil, errors.Wrap(err, "creating allowed counter")
	}

	rejectedCounter, err := mp.NewInt64Counter(inMemoryName + "_rejected")
	if err != nil {
		return nil, errors.Wrap(err, "creating rejected counter")
	}

	// Two eviction counters rather than one, because they mean opposite things.
	// An idle eviction reclaims a bucket that had refilled anyway and changes no
	// decision; a capacity eviction forgives a bucket that had not, so a
	// non-zero rate on it says the bound is being hit and some keys are getting
	// their allowance back early.
	idleEvictedCounter, err := mp.NewInt64Counter(inMemoryName + "_limiters_evicted_idle")
	if err != nil {
		return nil, errors.Wrap(err, "creating idle eviction counter")
	}

	capacityEvictedCounter, err := mp.NewInt64Counter(inMemoryName + "_limiters_evicted_capacity")
	if err != nil {
		return nil, errors.Wrap(err, "creating capacity eviction counter")
	}

	limitersGauge, err := mp.NewInt64Gauge(inMemoryName + "_limiters")
	if err != nil {
		return nil, errors.Wrap(err, "creating tracked limiters gauge")
	}

	window := limiterWindow(requestsPerSec, burstSize)

	r := &InMemoryRateLimiter{
		o11y:                   observability.NewObserver(inMemoryName, o.logger, o.tracerProvider),
		clock:                  o.clock,
		requestsPerSec:         requestsPerSec,
		burstSize:              burstSize,
		maxLimiters:            o.maxLimiters,
		idleTTL:                2 * window,
		sweepInterval:          max(window, minSweepInterval),
		allowedCounter:         allowedCounter,
		rejectedCounter:        rejectedCounter,
		idleEvictedCounter:     idleEvictedCounter,
		capacityEvictedCounter: capacityEvictedCounter,
		limitersGauge:          limitersGauge,
		stop:                   make(chan struct{}),
		done:                   make(chan struct{}),
	}

	// Started last, so the sweep cannot observe a half-built limiter. Its
	// lifetime is the limiter's rather than any caller context's: eviction is
	// not something a caller opts into, and Close is already the lifecycle hook
	// the interface gives them.
	go r.sweepEvery()

	return r, nil
}

func (r *InMemoryRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	limiter := r.getOrCreateLimiter(ctx, key)
	allowed := limiter.Allow()
	if allowed {
		r.allowedCounter.Add(ctx, 1)
	} else {
		r.rejectedCounter.Add(ctx, 1)
	}
	return allowed, nil
}

// RetryAfter reports how long key's bucket needs to hold a whole token again.
//
// It reads the bucket rather than reserving from it. rate.Limiter.Reserve
// answers the same question, but spends the token to do it — so asking when to
// come back would itself push the answer further out, and a refused caller that
// asked twice would be told to wait longer for having asked.
//
// It does not count as touching the key either: this is the refusal path, so
// the Allow that produced the refusal has already stamped the key, and a hint
// that kept a bucket resident would let a client hold a limiter open by asking
// when it may return.
//
// A key with no bucket yet reports no hint rather than zero: the caller is
// about to be allowed, so there is nothing to wait for and nothing to say.
func (r *InMemoryRateLimiter) RetryAfter(_ context.Context, key string) (time.Duration, bool) {
	entry, ok := r.lookup(key)
	if !ok {
		return 0, false
	}

	limiter := entry.limiter

	// A bucket that cannot hold a token never fills, so no wait would make the
	// next attempt succeed. Saying nothing is the honest answer.
	if limiter.Burst() < 1 {
		return 0, false
	}

	limit := float64(limiter.Limit())
	if limit <= 0 {
		return 0, false
	}

	deficit := 1 - limiter.Tokens()
	if deficit <= 0 || math.IsInf(limit, 1) {
		return 0, true
	}

	return time.Duration(deficit / limit * float64(time.Second)), true
}

// lookup returns key's entry without stamping it.
func (r *InMemoryRateLimiter) lookup(key string) (*limiterEntry, bool) {
	value, ok := r.limiters.Load(key)
	if !ok {
		return nil, false
	}

	entry, ok := value.(*limiterEntry)

	return entry, ok
}

func (r *InMemoryRateLimiter) getOrCreateLimiter(ctx context.Context, key string) *rate.Limiter {
	now := r.clock.Now()

	if entry, ok := r.lookup(key); ok {
		entry.touch(now)

		return entry.limiter
	}

	fresh := newLimiterEntry(rate.NewLimiter(rate.Limit(r.requestsPerSec), r.burstSize), now)

	if value, loaded := r.limiters.LoadOrStore(key, fresh); loaded {
		if entry, ok := value.(*limiterEntry); ok {
			entry.touch(now)

			return entry.limiter
		}

		return fresh.limiter
	}

	// This call is the one that added the key, so it is the one that counts it
	// and the one that has to answer for the bound.
	r.tracked.Add(1)
	r.evictOverflow(ctx)

	return fresh.limiter
}

// Close stops the sweeper and drops every per-key limiter.
//
// It is safe to call more than once, and it waits for the sweeper to exit, so a
// caller that closes a limiter holds no goroutine of ours afterwards.
func (r *InMemoryRateLimiter) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	<-r.done

	// Drop every per-key limiter so the map doesn't retain memory past shutdown.
	r.limiters.Clear()
	r.tracked.Store(0)

	return nil
}

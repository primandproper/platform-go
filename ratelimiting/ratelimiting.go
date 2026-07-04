package ratelimiting

import (
	"context"
	"sync"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability/metrics"

	"golang.org/x/time/rate"
)

const inMemoryName = "in_memory_rate_limiter"

// RateLimiter limits the rate of operations per key.
type RateLimiter interface {
	Allow(ctx context.Context, key string) (bool, error)
	Close() error
}

type inMemoryRateLimiter struct {
	allowedCounter  metrics.Int64Counter
	rejectedCounter metrics.Int64Counter
	limiters        sync.Map
	requestsPerSec  float64
	burstSize       int
}

// NewInMemoryRateLimiter returns a RateLimiter that uses per-key limiters in memory.
func NewInMemoryRateLimiter(metricsProvider metrics.Provider, requestsPerSec float64, burstSize int) (RateLimiter, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	allowedCounter, err := mp.NewInt64Counter(inMemoryName + "_allowed")
	if err != nil {
		return nil, errors.Wrap(err, "creating allowed counter")
	}

	rejectedCounter, err := mp.NewInt64Counter(inMemoryName + "_rejected")
	if err != nil {
		return nil, errors.Wrap(err, "creating rejected counter")
	}

	return &inMemoryRateLimiter{
		requestsPerSec:  requestsPerSec,
		burstSize:       burstSize,
		allowedCounter:  allowedCounter,
		rejectedCounter: rejectedCounter,
	}, nil
}

func (r *inMemoryRateLimiter) Allow(ctx context.Context, key string) (bool, error) {
	limiter := r.getOrCreateLimiter(ctx, key)
	allowed := limiter.Allow()
	if allowed {
		r.allowedCounter.Add(ctx, 1)
	} else {
		r.rejectedCounter.Add(ctx, 1)
	}
	return allowed, nil
}

func (r *inMemoryRateLimiter) getOrCreateLimiter(_ context.Context, key string) *rate.Limiter {
	if v, ok := r.limiters.Load(key); ok {
		if x, ok2 := v.(*rate.Limiter); ok2 {
			return x
		}
	}

	limiter := rate.NewLimiter(rate.Limit(r.requestsPerSec), r.burstSize)
	if v, loaded := r.limiters.LoadOrStore(key, limiter); loaded {
		if x, ok2 := v.(*rate.Limiter); ok2 {
			return x
		}
	}

	return limiter
}

func (r *inMemoryRateLimiter) Close() error {
	// Drop every per-key limiter so the map doesn't retain memory past shutdown.
	r.limiters.Clear()
	return nil
}

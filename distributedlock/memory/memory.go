package memory

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v2/distributedlock"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/identifiers"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

const serviceName = "in_memory_distributed_lock"

var (
	_ distributedlock.Locker = (*locker)(nil)
	_ distributedlock.Lock   = (*lock)(nil)
)

// held tracks the current owner of a key.
type held struct {
	expires time.Time
	token   string
}

// locker is a single-process distributedlock.Locker. It uses a sync.Mutex over an
// in-memory map and lazy expiration on each Acquire — there is no background
// goroutine. It is intended for tests, single-replica deployments, and as a clear
// reference implementation of the lock semantics.
type locker struct {
	o11y           observability.Observer
	held           map[string]*held
	acquireCounter metrics.Int64Counter
	releaseCounter metrics.Int64Counter
	refreshCounter metrics.Int64Counter
	contendCounter metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	mu             sync.Mutex
}

// NewLocker constructs a new in-memory Locker.
func NewLocker(
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (distributedlock.Locker, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	acquireCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_acquires", serviceName))
	if err != nil {
		return nil, errors.Wrap(err, "creating acquire counter")
	}
	releaseCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_releases", serviceName))
	if err != nil {
		return nil, errors.Wrap(err, "creating release counter")
	}
	refreshCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_refreshes", serviceName))
	if err != nil {
		return nil, errors.Wrap(err, "creating refresh counter")
	}
	contendCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_contended", serviceName))
	if err != nil {
		return nil, errors.Wrap(err, "creating contention counter")
	}
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	return &locker{
		o11y:           observability.NewObserver(serviceName, logger, tracerProvider),
		held:           make(map[string]*held),
		acquireCounter: acquireCounter,
		releaseCounter: releaseCounter,
		refreshCounter: refreshCounter,
		contendCounter: contendCounter,
		latencyHist:    latencyHist,
	}, nil
}

// Acquire implements distributedlock.Locker.
func (l *locker) Acquire(ctx context.Context, key string, ttl time.Duration) (distributedlock.Lock, error) {
	ctx, op := l.o11y.Begin(ctx)
	defer op.End()

	op.Set("lock.key", key).Set("lock.ttl", ttl)

	if key == "" {
		return nil, distributedlock.ErrEmptyKey
	}
	if ttl <= 0 {
		return nil, distributedlock.ErrInvalidTTL
	}

	startTime := time.Now()
	defer func() {
		l.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	l.mu.Lock()
	defer l.mu.Unlock()

	if existing, ok := l.held[key]; ok && time.Now().Before(existing.expires) {
		l.contendCounter.Add(ctx, 1)
		return nil, distributedlock.ErrLockNotAcquired
	}

	token := identifiers.New()
	l.held[key] = &held{token: token, expires: time.Now().Add(ttl)}
	l.acquireCounter.Add(ctx, 1)

	return &lock{
		locker: l,
		key:    key,
		token:  token,
		ttl:    ttl,
	}, nil
}

// Ping implements distributedlock.Locker.
func (*locker) Ping(_ context.Context) error {
	return nil
}

// Close drops all currently held locks. After Close, outstanding handles will see
// ErrLockNotHeld on Release/Refresh.
func (l *locker) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = make(map[string]*held)
	return nil
}

// release is the internal release path called by lock handles. It runs under the
// locker's mutex and verifies the token still owns the key.
func (l *locker) release(ctx context.Context, key, token string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	current, ok := l.held[key]
	if !ok || current.token != token || time.Now().After(current.expires) {
		return distributedlock.ErrLockNotHeld
	}
	delete(l.held, key)
	l.releaseCounter.Add(ctx, 1)
	return nil
}

// refresh is the internal refresh path called by lock handles. It runs under the
// locker's mutex and verifies the token still owns the key before extending TTL.
func (l *locker) refresh(ctx context.Context, key, token string, ttl time.Duration) error {
	if ttl <= 0 {
		return distributedlock.ErrInvalidTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	current, ok := l.held[key]
	if !ok || current.token != token || time.Now().After(current.expires) {
		return distributedlock.ErrLockNotHeld
	}
	current.expires = time.Now().Add(ttl)
	l.refreshCounter.Add(ctx, 1)
	return nil
}

// lock is the in-memory Lock handle.
type lock struct {
	locker *locker
	key    string
	token  string
	ttl    time.Duration
}

// Key implements distributedlock.Lock.
func (l *lock) Key() string { return l.key }

// TTL implements distributedlock.Lock.
func (l *lock) TTL() time.Duration { return l.ttl }

// Release implements distributedlock.Lock.
func (l *lock) Release(ctx context.Context) error {
	return l.locker.release(ctx, l.key, l.token)
}

// Refresh implements distributedlock.Lock.
func (l *lock) Refresh(ctx context.Context, ttl time.Duration) error {
	if err := l.locker.refresh(ctx, l.key, l.token, ttl); err != nil {
		return err
	}
	l.ttl = ttl
	return nil
}

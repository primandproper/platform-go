package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v5/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v5/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v5/distributedlock"
	"github.com/primandproper/platform-go/v5/identifiers"
	"github.com/primandproper/platform-go/v5/observability"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/testutils/containers"
	"github.com/primandproper/platform-go/v5/testutils/containers/redistest"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func buildContainerBackedRedisConfig(t *testing.T) *Config {
	t.Helper()

	container := redistest.Start(t)
	return &Config{
		Addresses: []string{redistest.Address(t, container)},
		KeyPrefix: "lock:",
	}
}

func newTestLocker(t *testing.T, cfg *Config) distributedlock.Locker {
	t.Helper()
	l, err := NewRedisLocker(cfg, nil, nil, nil, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, l)
	return l
}

// directRedisClient builds a raw go-redis client against the same address. Tests
// use it to forge ownership tokens and verify the wrong-owner branch.
func directRedisClient(t *testing.T, cfg *Config) *redis.Client {
	t.Helper()
	return redis.NewClient(&redis.Options{
		Addr:     cfg.Addresses[0],
		Username: cfg.Username,
		Password: cfg.Password,
	})
}

// --------- unit tests (no container) ---------

// fakeRedisClient is a hand-written stand-in for the redisClient interface so
// the locker logic can be exercised without a real Redis. Each command kind has
// a configurable result + error, and call counts are recorded for assertions.
type fakeRedisClient struct {
	setNXErr    error
	evalErr     error
	pingErr     error
	closeErr    error
	lastSetVal  any
	lastSetKey  string
	lastEvalKey string
	lastEvalArg []any
	setNXCalls  int
	closeCalls  int
	pingCalls   int
	evalCalls   int
	lastSetTTL  time.Duration
	evalResult  int64
	setNXResult bool
}

func (f *fakeRedisClient) SetNX(ctx context.Context, key string, value any, expiration time.Duration) *redis.BoolCmd {
	f.setNXCalls++
	f.lastSetKey = key
	f.lastSetVal = value
	f.lastSetTTL = expiration
	cmd := redis.NewBoolCmd(ctx)
	cmd.SetVal(f.setNXResult)
	if f.setNXErr != nil {
		cmd.SetErr(f.setNXErr)
	}
	return cmd
}

func (f *fakeRedisClient) Eval(ctx context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	f.evalCalls++
	if len(keys) > 0 {
		f.lastEvalKey = keys[0]
	}
	f.lastEvalArg = args
	cmd := redis.NewCmd(ctx)
	cmd.SetVal(f.evalResult)
	if f.evalErr != nil {
		cmd.SetErr(f.evalErr)
	}
	return cmd
}

func (f *fakeRedisClient) Ping(ctx context.Context) *redis.StatusCmd {
	f.pingCalls++
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetVal("PONG")
	if f.pingErr != nil {
		cmd.SetErr(f.pingErr)
	}
	return cmd
}

func (f *fakeRedisClient) Close() error {
	f.closeCalls++
	return f.closeErr
}

// errorAtCallProvider wraps a noop metrics provider but injects errors at a
// specific Int64Counter call index or on the Float64Histogram call. It exists
// so the constructor's metric-creation error branches can be exercised.
type errorAtCallProvider struct {
	metrics.Provider
	errOnInt64Counter     int
	int64CallCount        int
	errOnFloat64Histogram bool
}

func newErrorAtCallProvider(int64FailIdx int, histFail bool) *errorAtCallProvider {
	return &errorAtCallProvider{
		Provider:              metricsnoop.NewMetricsProvider(),
		errOnInt64Counter:     int64FailIdx,
		errOnFloat64Histogram: histFail,
	}
}

func (p *errorAtCallProvider) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	p.int64CallCount++
	if p.errOnInt64Counter == p.int64CallCount {
		return nil, errors.New("simulated counter error")
	}
	return p.Provider.NewInt64Counter(name, options...)
}

func (p *errorAtCallProvider) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	if p.errOnFloat64Histogram {
		return nil, errors.New("simulated histogram error")
	}
	return p.Provider.NewFloat64Histogram(name, options...)
}

// newUnitLocker constructs a *locker directly with a fake client so unit tests
// can exercise the per-method logic without going through buildRedisClient.
func newUnitLocker(t *testing.T, client redisClient, cb circuitbreaking.CircuitBreaker) *locker {
	t.Helper()
	mp := metricsnoop.NewMetricsProvider()
	acquireCounter, err := mp.NewInt64Counter("redis_distributed_lock_acquires")
	must.NoError(t, err)
	releaseCounter, err := mp.NewInt64Counter("redis_distributed_lock_releases")
	must.NoError(t, err)
	refreshCounter, err := mp.NewInt64Counter("redis_distributed_lock_refreshes")
	must.NoError(t, err)
	contendCounter, err := mp.NewInt64Counter("redis_distributed_lock_contended")
	must.NoError(t, err)
	errCounter, err := mp.NewInt64Counter("redis_distributed_lock_errors")
	must.NoError(t, err)
	latencyHist, err := mp.NewFloat64Histogram("redis_distributed_lock_latency_ms")
	must.NoError(t, err)
	if cb == nil {
		cb = cbnoop.NewCircuitBreaker()
	}
	return &locker{
		o11y:           observability.NewObserver("test", loggingnoop.NewLogger(), tracingnoop.NewTracerProvider()),
		client:         client,
		circuitBreaker: cb,
		acquireCounter: acquireCounter,
		releaseCounter: releaseCounter,
		refreshCounter: refreshCounter,
		contendCounter: contendCounter,
		errCounter:     errCounter,
		latencyHist:    latencyHist,
		keyPrefix:      "lock:",
	}
}

// newRecordingLocker builds a *locker with a RecordingObserver swapped in, so a
// test can drive a method and assert that its operation ran (and recorded errors
// on failure paths).
func newRecordingLocker(t *testing.T, client redisClient, cb circuitbreaking.CircuitBreaker) (*locker, *observability.RecordingObserver) {
	t.Helper()
	l := newUnitLocker(t, client, cb)
	obs := observability.NewRecordingObserver()
	l.o11y = obs
	return l, obs
}

func TestNewRedisLocker(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		_, err := NewRedisLocker(nil, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, distributedlock.ErrNilConfig)
	})

	T.Run("empty addresses", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: nil, KeyPrefix: "lock:"}
		l, err := NewRedisLocker(cfg, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
		test.Nil(t, l)
	})

	T.Run("standard happy path", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:0"}, KeyPrefix: "lock:"}
		l, err := NewRedisLocker(cfg, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, l)
		must.NoError(t, l.Close())
	})

	T.Run("cluster mode happy path", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:0", "localhost:1"}, KeyPrefix: "lock:"}
		l, err := NewRedisLocker(cfg, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, l)
		must.NoError(t, l.Close())
	})

	// Each metric counter creation has its own error branch; exercise them all so
	// no error path is left untested.
	for idx := 1; idx <= 5; idx++ {
		T.Run("int64 counter creation failure", func(t *testing.T) {
			t.Parallel()
			cfg := &Config{Addresses: []string{"localhost:0"}}
			mp := newErrorAtCallProvider(idx, false)
			_, err := NewRedisLocker(cfg, nil, nil, mp, cbnoop.NewCircuitBreaker())
			must.Error(t, err)
		})
	}

	T.Run("float64 histogram creation failure", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:0"}}
		mp := newErrorAtCallProvider(0, true)
		_, err := NewRedisLocker(cfg, nil, nil, mp, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})
}

func TestLocker_Acquire(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		l, obs := newRecordingLocker(t, fc, nil)

		got, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, "k", got.Key())
		test.EqOp(t, time.Minute, got.TTL())
		test.EqOp(t, "lock:k", fc.lastSetKey)
		test.EqOp(t, time.Minute, fc.lastSetTTL)

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceEmpty(t, op.Errors)
	})

	T.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		l := newUnitLocker(t, &fakeRedisClient{}, nil)
		_, err := l.Acquire(t.Context(), "", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
	})

	T.Run("rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		l := newUnitLocker(t, &fakeRedisClient{}, nil)
		_, err := l.Acquire(t.Context(), "k", 0)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("rejects negative TTL", func(t *testing.T) {
		t.Parallel()
		l := newUnitLocker(t, &fakeRedisClient{}, nil)
		_, err := l.Acquire(t.Context(), "k", -time.Second)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}
		l := newUnitLocker(t, &fakeRedisClient{}, cb)

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		must.SliceNotEmpty(t, cb.CannotProceedCalls())
	})

	T.Run("SetNX backend error trips breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXErr: errors.New("redis down")}
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}
		l, obs := newRecordingLocker(t, fc, cb)

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.Error(t, err)
		must.SliceNotEmpty(t, cb.CannotProceedCalls())
		must.SliceNotEmpty(t, cb.FailedCalls())

		// The backend failure must be recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("contention does not fail breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: false}
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}
		l := newUnitLocker(t, fc, cb)

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		must.SliceNotEmpty(t, cb.CannotProceedCalls())
		must.SliceNotEmpty(t, cb.SucceededCalls())
	})
}

func TestLocker_Release(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true, evalResult: 1}
		l := newUnitLocker(t, fc, nil)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, h.Release(t.Context()))
		test.EqOp(t, "lock:k", fc.lastEvalKey)
	})

	T.Run("eval reports caller no longer holds lock", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true, evalResult: 0}
		l := newUnitLocker(t, fc, nil)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Release(t.Context()), distributedlock.ErrLockNotHeld)
	})

	T.Run("eval backend error trips breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		// Acquire path: proceed + succeeded. Release path: proceed + failed.
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
			FailedFunc:        func() {},
		}
		l := newUnitLocker(t, fc, cb)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		fc.evalErr = errors.New("eval boom")
		must.Error(t, h.Release(t.Context()))
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
		must.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		var cannotProceedCalls int
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool {
				cannotProceedCalls++
				return cannotProceedCalls > 1 // first call (Acquire) proceeds, second (Release) is blocked
			},
			SucceededFunc: func() {},
		}
		l := newUnitLocker(t, fc, cb)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Release(t.Context()), circuitbreaking.ErrCircuitBroken)
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestLocker_Refresh(T *testing.T) {
	T.Parallel()

	T.Run("happy path updates TTL", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true, evalResult: 1}
		l := newUnitLocker(t, fc, nil)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		must.NoError(t, h.Refresh(t.Context(), 5*time.Minute))
		test.EqOp(t, 5*time.Minute, h.TTL())
	})

	T.Run("rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		l := newUnitLocker(t, fc, nil)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Refresh(t.Context(), 0), distributedlock.ErrInvalidTTL)
		// TTL must remain unchanged on failure.
		test.EqOp(t, time.Minute, h.TTL())
	})

	T.Run("eval reports caller no longer holds lock", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true, evalResult: 0}
		l := newUnitLocker(t, fc, nil)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		// Force the refresh script to "not held" by returning 0.
		must.ErrorIs(t, h.Refresh(t.Context(), 2*time.Minute), distributedlock.ErrLockNotHeld)
		test.EqOp(t, time.Minute, h.TTL())
	})

	T.Run("eval backend error trips breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
			FailedFunc:        func() {},
		}
		l := newUnitLocker(t, fc, cb)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		fc.evalErr = errors.New("eval boom")
		must.Error(t, h.Refresh(t.Context(), 5*time.Minute))
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
		must.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{setNXResult: true}
		var cannotProceedCalls int
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool {
				cannotProceedCalls++
				return cannotProceedCalls > 1 // first call (Acquire) proceeds, second (Refresh) is blocked
			},
			SucceededFunc: func() {},
		}
		l := newUnitLocker(t, fc, cb)

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Refresh(t.Context(), time.Minute), circuitbreaking.ErrCircuitBroken)
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestLocker_PingClose(T *testing.T) {
	T.Parallel()

	T.Run("ping success", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{}
		l := newUnitLocker(t, fc, nil)
		must.NoError(t, l.Ping(t.Context()))
		test.EqOp(t, 1, fc.pingCalls)
	})

	T.Run("ping error", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{pingErr: errors.New("ping boom")}
		l := newUnitLocker(t, fc, nil)
		must.Error(t, l.Ping(t.Context()))
	})

	T.Run("close success", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{}
		l := newUnitLocker(t, fc, nil)
		must.NoError(t, l.Close())
		test.EqOp(t, 1, fc.closeCalls)
	})

	T.Run("close error", func(t *testing.T) {
		t.Parallel()
		fc := &fakeRedisClient{closeErr: errors.New("close boom")}
		l := newUnitLocker(t, fc, nil)
		must.Error(t, l.Close())
	})
}

func TestBuildRedisClient(T *testing.T) {
	T.Parallel()

	T.Run("single address builds standalone client", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:6379"}}
		c := buildRedisClient(cfg)
		must.NotNil(t, c)
		_, ok := c.(*redis.Client)
		test.True(t, ok)
		_ = c.Close()
	})

	T.Run("multiple addresses builds cluster client", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:6379", "localhost:6380"}}
		c := buildRedisClient(cfg)
		must.NotNil(t, c)
		_, ok := c.(*redis.ClusterClient)
		test.True(t, ok)
		_ = c.Close()
	})
}

// --------- container-backed integration tests ---------

func TestRedisLocker_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	cfg := buildContainerBackedRedisConfig(T)

	T.Run("Acquire happy path", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "happy_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NotNil(t, lock)
		test.EqOp(t, key, lock.Key())
		test.EqOp(t, time.Minute, lock.TTL())

		must.NoError(t, lock.Release(ctx))
	})

	T.Run("Acquire contended returns ErrLockNotAcquired", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "contended_" + identifiers.New()

		first, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = first.Release(ctx) })

		_, err = l.Acquire(ctx, key, time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	T.Run("Acquire rejects empty key", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, cfg)
		_, err := l.Acquire(t.Context(), "", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
	})

	T.Run("Acquire rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, cfg)
		_, err := l.Acquire(t.Context(), "k", 0)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("Release after expiration returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "expired_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, 100*time.Millisecond)
		must.NoError(t, err)
		time.Sleep(250 * time.Millisecond)

		must.ErrorIs(t, lock.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	T.Run("Release wrong owner returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "stolen_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)

		// Forge a different owner by overwriting the value out-of-band.
		direct := directRedisClient(t, cfg)
		t.Cleanup(func() { _ = direct.Close() })
		must.NoError(t, direct.Set(ctx, "lock:"+key, "someone-else", time.Minute).Err())

		must.ErrorIs(t, lock.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	T.Run("Refresh extends TTL", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "refresh_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, 200*time.Millisecond)
		must.NoError(t, err)
		must.NoError(t, lock.Refresh(ctx, 5*time.Second))
		t.Cleanup(func() { _ = lock.Release(ctx) })

		// Sleep past the original TTL; lock should still be held.
		time.Sleep(300 * time.Millisecond)

		_, err = l.Acquire(ctx, key, time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		test.EqOp(t, 5*time.Second, lock.TTL())
	})

	T.Run("Refresh rejects invalid TTL", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "refreshinv_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = lock.Release(ctx) })

		must.ErrorIs(t, lock.Refresh(ctx, 0), distributedlock.ErrInvalidTTL)
	})

	T.Run("Double release returns ErrLockNotHeld on second call", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "double_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, lock.Release(ctx))
		must.ErrorIs(t, lock.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	T.Run("Released lock can be reacquired", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, cfg)
		key := "reacquire_" + identifiers.New()

		first, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, first.Release(ctx))

		second, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, second.Release(ctx))
	})

	T.Run("Ping success", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, cfg)
		must.NoError(t, l.Ping(t.Context()))
	})
}

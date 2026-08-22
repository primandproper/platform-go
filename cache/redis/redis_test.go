package redis

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/testutils/containers/redistest"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

const exampleKey = "example"

type example struct {
	Name string `json:"name"`
}

func gobEncodeExample(t *testing.T, e *example) string {
	t.Helper()

	var buf bytes.Buffer
	must.NoError(t, gob.NewEncoder(&buf).Encode(e))

	return buf.String()
}

func buildTestImpl(t *testing.T) (*Cache[example], *redisClientMock, *circuitbreakingmock.CircuitBreakerMock, *observability.RecordingObserver) {
	t.Helper()

	mp := metricsnoop.NewMetricsProvider()

	hitCounter, err := mp.NewInt64Counter("test_hits")
	must.NoError(t, err)

	missCounter, err := mp.NewInt64Counter("test_misses")
	must.NoError(t, err)

	setCounter, err := mp.NewInt64Counter("test_sets")
	must.NoError(t, err)

	delCounter, err := mp.NewInt64Counter("test_deletes")
	must.NoError(t, err)

	errCounter, err := mp.NewInt64Counter("test_errors")
	must.NoError(t, err)

	latencyHist, err := mp.NewFloat64Histogram("test_latency")
	must.NoError(t, err)

	client := &redisClientMock{}
	cb := &circuitbreakingmock.CircuitBreakerMock{}
	obs := observability.NewRecordingObserver()

	return &Cache[example]{
		o11y:             obs,
		codec:            cache.NewGobCodec[example](),
		cacheHitCounter:  hitCounter,
		cacheMissCounter: missCounter,
		cacheSetCounter:  setCounter,
		cacheDelCounter:  delCounter,
		cacheErrCounter:  errCounter,
		latencyHist:      latencyHist,
		client:           client,
		circuitBreaker:   cb,
		expiration:       time.Minute,
		scanPageSize:     defaultScanPageSize,
	}, client, cb, obs
}

// counterResult bundles the values a mocked NewInt64Counter call returns.
type counterResult struct {
	counter metrics.Int64Counter
	err     error
}

// newCounterProviderMock returns a metrics.Provider mock whose NewInt64Counter
// implementation looks up the result keyed on the counter name. Unknown names
// fail the test.
func newCounterProviderMock(t *testing.T, results map[string]counterResult) *metricsmock.ProviderMock {
	t.Helper()
	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(metricName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			res, ok := results[metricName]
			if !ok {
				t.Fatalf("unexpected NewInt64Counter call: %q", metricName)
			}
			return res.counter, res.err
		},
	}
}

func buildContainerBackedRedisConfig(t *testing.T) *Config {
	t.Helper()

	container := redistest.Start(t)
	return &Config{
		Addresses: []string{redistest.Address(t, container)},
	}
}

func TestNewRedisCache(T *testing.T) {
	T.Parallel()

	okCounter := func() metrics.Int64Counter { return metricstest.Int64Counter(T, "x") }

	T.Run("with no addresses", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](&Config{}, time.Minute, nil)
		test.Error(t, err)
		test.Nil(t, c)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](nil, time.Minute, nil)
		test.Error(t, err)
		test.Nil(t, c)
	})

	T.Run("with single address", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		c, err := NewRedisCache[example](cfg, time.Minute, nil)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with multiple addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379", "localhost:6380"}}

		c, err := NewRedisCache[example](cfg, time.Minute, nil)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with error creating cache hit counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache miss counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":   {counter: okCounter()},
			name + "_cache_misses": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache set counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":   {counter: okCounter()},
			name + "_cache_misses": {counter: okCounter()},
			name + "_cache_sets":   {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache delete counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":    {counter: okCounter()},
			name + "_cache_misses":  {counter: okCounter()},
			name + "_cache_sets":    {counter: okCounter()},
			name + "_cache_deletes": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 4, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache error counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":    {counter: okCounter()},
			name + "_cache_misses":  {counter: okCounter()},
			name + "_cache_sets":    {counter: okCounter()},
			name + "_cache_deletes": {counter: okCounter()},
			name + "_cache_errors":  {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Addresses: []string{"localhost:6379"}}

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(metricName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_cache_latency_ms", metricName)
				return h, errors.New("histogram error")
			},
		}

		c, err := NewRedisCache[example](cfg, time.Minute, nil, WithMetricsProvider(mp))
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func Test_Cache_Get(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))

		actual, getErr := c.Get(ctx, exampleKey)
		test.Eq(t, exampleContent, actual)
		test.NoError(t, getErr)
	})
}

func Test_Cache_Get_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		expected := &example{Name: t.Name()}
		encoded := gobEncodeExample(t, expected)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.GetFunc = func(_ context.Context, key string) *redis.StringCmd {
			test.EqOp(t, exampleKey, key)
			cmd := redis.NewStringCmd(ctx)
			cmd.SetVal(encoded)
			return cmd
		}

		actual, err := impl.Get(ctx, exampleKey)
		test.NoError(t, err)
		test.Eq(t, expected, actual)

		test.SliceLen(t, 1, client.GetCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{})
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, _, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		// Not ErrNotFound: a caller that must distinguish "absent" from
		// "unreachable" — idempotency's FailClosed — has to be able to.
		actual, err := impl.Get(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrUnavailable)
		test.Nil(t, actual)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with cache miss", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}
		cb.FailedFunc = func() {}

		client.GetFunc = func(_ context.Context, key string) *redis.StringCmd {
			test.EqOp(t, exampleKey, key)
			cmd := redis.NewStringCmd(ctx)
			cmd.SetErr(redis.Nil)
			return cmd
		}

		actual, err := impl.Get(ctx, exampleKey)
		// A miss is the sentinel callers check for, not a wrapped infra error.
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.Nil(t, actual)

		test.SliceLen(t, 1, client.GetCalls())
		// A miss is a healthy response: the breaker records success, not failure.
		test.SliceLen(t, 1, cb.SucceededCalls())
		test.SliceLen(t, 0, cb.FailedCalls())

		// It must not be recorded as an operation error either.
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 0, op.Errors)
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.GetFunc = func(_ context.Context, key string) *redis.StringCmd {
			test.EqOp(t, exampleKey, key)
			cmd := redis.NewStringCmd(ctx)
			cmd.SetErr(errors.New("redis error"))
			return cmd
		}

		actual, err := impl.Get(ctx, exampleKey)
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 1, client.GetCalls())
		test.SliceLen(t, 1, cb.FailedCalls())

		// The failure must be recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with decode error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }

		client.GetFunc = func(_ context.Context, key string) *redis.StringCmd {
			test.EqOp(t, exampleKey, key)
			cmd := redis.NewStringCmd(ctx)
			cmd.SetVal("not valid gob data")
			return cmd
		}

		actual, err := impl.Get(ctx, exampleKey)
		test.Error(t, err)
		test.Nil(t, actual)

		test.SliceLen(t, 1, client.GetCalls())
	})
}

func Test_Cache_Set(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))
	})
}

func Test_Cache_Set_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetFunc = func(_ context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
			test.EqOp(t, exampleKey, key)
			test.EqOp(t, time.Minute, expiration)
			_, isString := value.(string)
			test.True(t, isString)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}

		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()})
		test.NoError(t, err)

		test.SliceLen(t, 1, client.SetCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, _, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		// The write did not happen, so reporting success would be a lie.
		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()})
		test.ErrorIs(t, err, cache.ErrUnavailable)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.SetFunc = func(_ context.Context, key string, _ any, _ time.Duration) *redis.StatusCmd {
			test.EqOp(t, exampleKey, key)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetErr(errors.New("redis error"))
			return cmd
		}

		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()})
		test.Error(t, err)

		test.SliceLen(t, 1, client.SetCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func Test_Cache_SetIfPresent(T *testing.T) {
	T.Parallel()

	T.Run("against a real server", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil)
		must.NoError(t, err)

		// Absent: refused, and not created.
		test.ErrorIs(t, c.SetIfPresent(ctx, exampleKey, &example{Name: "after"}), cache.ErrNotFound)

		_, getErr := c.Get(ctx, exampleKey)
		test.ErrorIs(t, getErr, cache.ErrNotFound)

		// Present: overwritten.
		must.NoError(t, c.Set(ctx, exampleKey, &example{Name: "before"}))
		must.NoError(t, c.SetIfPresent(ctx, exampleKey, &example{Name: "after"}))

		got, err := c.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, "after", got.Name)

		// Deleted: refused again, which is the sign-out case this exists for.
		must.NoError(t, c.Delete(ctx, exampleKey))
		test.ErrorIs(t, c.SetIfPresent(ctx, exampleKey, &example{Name: "revived"}), cache.ErrNotFound)
	})
}

func Test_Cache_SetIfPresent_Unit(T *testing.T) {
	T.Parallel()

	T.Run("issues one SET with the XX flag", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetArgsFunc = func(_ context.Context, key string, value any, a redis.SetArgs) *redis.StatusCmd {
			test.EqOp(t, exampleKey, key)
			// XX is the whole guarantee: without it this is an ordinary Set that
			// resurrects a key somebody just deleted.
			test.EqOp(t, "XX", a.Mode)
			test.EqOp(t, time.Minute, a.TTL)
			test.False(t, a.KeepTTL)
			_, isString := value.(string)
			test.True(t, isString)

			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")

			return cmd
		}

		test.NoError(t, impl.SetIfPresent(ctx, exampleKey, &example{Name: t.Name()}))

		test.SliceLen(t, 1, client.SetArgsCalls())
		// Exactly one round trip. A Get here would mean the window is still open.
		test.SliceLen(t, 0, client.GetCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("a refusal is ErrNotFound and does not trip the breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetArgsFunc = func(context.Context, string, any, redis.SetArgs) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetErr(redis.Nil)

			return cmd
		}

		// The key was absent. That is the server answering correctly, not the
		// server being unwell — counting it as a failure would trip the breaker
		// for every caller on an entirely healthy redis.
		test.ErrorIs(t, impl.SetIfPresent(ctx, exampleKey, &example{Name: t.Name()}), cache.ErrNotFound)

		test.SliceLen(t, 1, cb.SucceededCalls())
		test.SliceLen(t, 0, cb.FailedCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		// ErrUnavailable rather than ErrNotFound. The two must not be conflated:
		// a caller told "not found" during an outage concludes the record is
		// gone, which for a session store means signing someone out.
		test.ErrorIs(t, impl.SetIfPresent(ctx, exampleKey, &example{Name: t.Name()}), cache.ErrUnavailable)

		test.SliceLen(t, 0, client.SetArgsCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.SetArgsFunc = func(context.Context, string, any, redis.SetArgs) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetErr(errors.New("redis error"))

			return cmd
		}

		err := impl.SetIfPresent(ctx, exampleKey, &example{Name: t.Name()})
		test.Error(t, err)
		// A transport failure must not masquerade as a refusal: the condition
		// was never evaluated, so nothing is known about whether the key exists.
		test.False(t, errors.Is(err, cache.ErrNotFound))

		test.SliceLen(t, 1, client.SetArgsCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func Test_Cache_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))

		test.NoError(t, c.Delete(ctx, exampleKey))
	})
}

func Test_Cache_Delete_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.DelFunc = func(_ context.Context, keys ...string) *redis.IntCmd {
			test.Eq(t, []string{exampleKey}, keys)
			cmd := redis.NewIntCmd(ctx)
			cmd.SetVal(1)
			return cmd
		}

		err := impl.Delete(ctx, exampleKey)
		test.NoError(t, err)

		test.SliceLen(t, 1, client.DelCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, _, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		// A dropped Delete reported as success serves the stale value for the
		// rest of its TTL.
		err := impl.Delete(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrUnavailable)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.DelFunc = func(_ context.Context, _ ...string) *redis.IntCmd {
			cmd := redis.NewIntCmd(ctx)
			cmd.SetErr(errors.New("redis error"))
			return cmd
		}

		err := impl.Delete(ctx, exampleKey)
		test.Error(t, err)

		test.SliceLen(t, 1, client.DelCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func Test_Cache_Ping_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.PingFunc = func(_ context.Context) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("PONG")
			return cmd
		}

		test.NoError(t, impl.Ping(ctx))
		test.SliceLen(t, 1, client.PingCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.PingFunc = func(_ context.Context) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetErr(errors.New("connection refused"))
			return cmd
		}

		test.Error(t, impl.Ping(ctx))
		test.SliceLen(t, 1, client.PingCalls())
		// The one call whose job is reporting reachability now feeds the breaker
		// and the span, rather than answering only the caller.
		test.SliceLen(t, 1, cb.FailedCalls())
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("an open breaker answers without touching redis", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		test.ErrorIs(t, impl.Ping(ctx), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.PingCalls())
	})
}

func Test_Cache_GetMany_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard with hit and miss", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		found := &example{Name: "found"}
		encoded := gobEncodeExample(t, found)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.MGetFunc = func(_ context.Context, keys ...string) *redis.SliceCmd {
			test.Eq(t, []string{"hit", "miss"}, keys)
			cmd := redis.NewSliceCmd(ctx)
			cmd.SetVal([]any{encoded, nil})
			return cmd
		}

		out, err := impl.GetMany(ctx, []string{"hit", "miss"})
		test.NoError(t, err)
		test.MapLen(t, 1, out)
		test.Eq(t, found, out["hit"])

		test.SliceLen(t, 1, client.MGetCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{})
	})

	T.Run("empty keys short-circuits", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, _, _ := buildTestImpl(t)

		out, err := impl.GetMany(ctx, nil)
		test.NoError(t, err)
		test.MapLen(t, 0, out)
		test.SliceLen(t, 0, client.MGetCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		out, err := impl.GetMany(ctx, []string{"a", "b"})
		test.ErrorIs(t, err, cache.ErrUnavailable)
		test.MapLen(t, 0, out)
		test.SliceLen(t, 0, client.MGetCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, obs := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.MGetFunc = func(_ context.Context, _ ...string) *redis.SliceCmd {
			cmd := redis.NewSliceCmd(ctx)
			cmd.SetErr(errors.New("redis error"))
			return cmd
		}

		out, err := impl.GetMany(ctx, []string{"a"})
		test.Error(t, err)
		test.Nil(t, out)
		test.SliceLen(t, 1, cb.FailedCalls())

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with decode error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }

		client.MGetFunc = func(_ context.Context, _ ...string) *redis.SliceCmd {
			cmd := redis.NewSliceCmd(ctx)
			cmd.SetVal([]any{"not valid gob data"})
			return cmd
		}

		out, err := impl.GetMany(ctx, []string{"a"})
		test.Error(t, err)
		test.Nil(t, out)
	})

	T.Run("cluster mode issues one MGET per slot", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.isCluster = true

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		// Distinct hashtags spread the keys across more than one slot.
		keys := []string{"{alpha}1", "{beta}2", "{alpha}3"}
		expectedGroups := len(groupBySlot(keys))
		must.Greater(t, 1, expectedGroups)

		client.MGetFunc = func(_ context.Context, mgetKeys ...string) *redis.SliceCmd {
			cmd := redis.NewSliceCmd(ctx)
			vals := make([]any, len(mgetKeys))
			cmd.SetVal(vals)
			return cmd
		}

		_, err := impl.GetMany(ctx, keys)
		test.NoError(t, err)
		test.SliceLen(t, expectedGroups, client.MGetCalls())
	})
}

func Test_Cache_SetMany_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.EvalFunc = func(_ context.Context, script string, keys []string, args ...any) *redis.Cmd {
			test.EqOp(t, batchSetScript, script)
			// ARGV[1] is the TTL in milliseconds; buildTestImpl uses a minute.
			ttl, ok := args[0].(int64)
			test.True(t, ok)
			test.EqOp(t, time.Minute.Milliseconds(), ttl)
			// One TTL arg plus one encoded value per key.
			test.SliceLen(t, len(keys)+1, args)
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(len(keys)))
			return cmd
		}

		err := impl.SetMany(ctx, map[string]*example{
			"a": {Name: "a"},
			"b": {Name: "b"},
		})
		test.NoError(t, err)
		test.SliceLen(t, 1, client.EvalCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("empty items short-circuits", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, _, _ := buildTestImpl(t)

		test.NoError(t, impl.SetMany(ctx, nil))
		test.SliceLen(t, 0, client.EvalCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		test.ErrorIs(t, impl.SetMany(ctx, map[string]*example{"a": {Name: "a"}}), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.EvalCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.EvalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetErr(errors.New("redis error"))
			return cmd
		}

		err := impl.SetMany(ctx, map[string]*example{"a": {Name: "a"}})
		test.Error(t, err)
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("cluster mode issues one EVAL per slot", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.isCluster = true

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		items := map[string]*example{
			"{alpha}1": {Name: "1"},
			"{beta}2":  {Name: "2"},
			"{alpha}3": {Name: "3"},
		}
		keys := make([]string, 0, len(items))
		for k := range items {
			keys = append(keys, k)
		}
		expectedGroups := len(groupBySlot(keys))
		must.Greater(t, 1, expectedGroups)

		client.EvalFunc = func(_ context.Context, _ string, keys []string, _ ...any) *redis.Cmd {
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(len(keys)))
			return cmd
		}

		test.NoError(t, impl.SetMany(ctx, items))
		test.SliceLen(t, expectedGroups, client.EvalCalls())
	})
}

func Test_Cache_SetMany_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("round trip against a real redis", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, time.Minute, nil)
		must.NoError(t, err)

		items := map[string]*example{
			"k1": {Name: "one"},
			"k2": {Name: "two"},
		}
		test.NoError(t, c.SetMany(ctx, items))

		out, getErr := c.GetMany(ctx, []string{"k1", "k2", "k3"})
		test.NoError(t, getErr)
		test.MapLen(t, 2, out)
		test.Eq(t, items["k1"], out["k1"])
		test.Eq(t, items["k2"], out["k2"])
	})
}

func Test_buildRedisClient(T *testing.T) {
	T.Parallel()

	T.Run("with single address", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Addresses: []string{"localhost:6379"},
			Username:  "user",
			Password:  "pass",
		}

		c, err := buildRedisClient(cfg)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with multiple addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Addresses: []string{"localhost:6379", "localhost:6380"},
			Username:  "user",
			Password:  "pass",
		}

		c, err := buildRedisClient(cfg)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with no addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Addresses: []string{},
		}

		// An empty address list is a configuration error, not a nil client that
		// panics on the first read.
		c, err := buildRedisClient(cfg)
		test.Error(t, err)
		test.Nil(t, c)
	})
}

func Test_Cache_Set_ExpiryOptions_Unit(T *testing.T) {
	T.Parallel()

	T.Run("WithExpiry overrides the configured default", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetFunc = func(_ context.Context, _ string, _ any, expiration time.Duration) *redis.StatusCmd {
			test.EqOp(t, 5*time.Minute, expiration)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}

		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()}, cache.WithExpiry(5*time.Minute))
		test.NoError(t, err)
		test.SliceLen(t, 1, client.SetCalls())
	})

	T.Run("NoExpiry stores without expiration", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetFunc = func(_ context.Context, _ string, _ any, expiration time.Duration) *redis.StatusCmd {
			test.EqOp(t, time.Duration(0), expiration)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}

		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()}, cache.WithExpiry(cache.NoExpiry))
		test.NoError(t, err)
		test.SliceLen(t, 1, client.SetCalls())
	})

	T.Run("SetMany forwards the resolved expiry to the batch script", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.EvalFunc = func(_ context.Context, _ string, _ []string, args ...any) *redis.Cmd {
			must.SliceNotEmpty(t, args)
			gotExpiry, ok := args[0].(int64)
			must.True(t, ok)
			test.EqOp(t, (5 * time.Minute).Milliseconds(), gotExpiry)
			cmd := redis.NewCmd(ctx)
			cmd.SetVal(int64(1))
			return cmd
		}

		err := impl.SetMany(ctx, map[string]*example{exampleKey: {Name: t.Name()}}, cache.WithExpiry(5*time.Minute))
		test.NoError(t, err)
		test.SliceLen(t, 1, client.EvalCalls())
	})
}

// nameCodec stores only the Name field as raw bytes — a stand-in for a
// consumer-supplied fixed-format codec.
type nameCodec struct{}

func (nameCodec) Encode(value *example) ([]byte, error) {
	return []byte(value.Name), nil
}

func (nameCodec) Decode(data []byte) (*example, error) {
	return &example{Name: string(data)}, nil
}

// nilCodec decodes every payload to a nil value without erroring, standing in
// for a codec whose wire form has a legitimate "absent" encoding. The cache
// treats that as a miss rather than handing back a nil pointer.
type nilCodec struct{}

func (nilCodec) Encode(*example) ([]byte, error) { return []byte("whatever"), nil }

func (nilCodec) Decode([]byte) (*example, error) { return nil, nil }

// brokenCodec fails to encode, standing in for a value the configured codec
// cannot represent.
type brokenCodec struct{}

var errCodecBroken = errors.New("codec cannot encode this")

func (brokenCodec) Encode(*example) ([]byte, error) { return nil, errCodecBroken }

func (brokenCodec) Decode([]byte) (*example, error) { return nil, errCodecBroken }

func Test_Cache_OpenCircuit_Unit(T *testing.T) {
	T.Parallel()

	// An open breaker skips the round trip on every write path and says so.
	// It used to return nil, which reported a write that never happened — and a
	// dropped Delete reported as success serves the stale value for the rest of
	// its TTL.
	openBreaker := func(t *testing.T) (*Cache[example], *redisClientMock) {
		t.Helper()

		impl, client, cb, _ := buildTestImpl(t)
		cb.CannotProceedFunc = func() bool { return true }

		return impl, client
	}

	T.Run("Set reports unavailability", func(t *testing.T) {
		t.Parallel()

		impl, client := openBreaker(t)

		test.ErrorIs(t, impl.Set(t.Context(), exampleKey, &example{Name: "spot"}), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.SetCalls())
	})

	T.Run("DeleteMany reports unavailability", func(t *testing.T) {
		t.Parallel()

		impl, client := openBreaker(t)

		test.ErrorIs(t, impl.DeleteMany(t.Context(), []string{"a", "b"}), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.DelCalls())
	})

	T.Run("DeleteByPrefix reports unavailability", func(t *testing.T) {
		t.Parallel()

		impl, client := openBreaker(t)
		impl.namespace = "ns:"

		test.ErrorIs(t, impl.DeleteByPrefix(t.Context(), "p:"), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.ScanCalls())
	})

	T.Run("SetMany reports unavailability", func(t *testing.T) {
		t.Parallel()

		impl, client := openBreaker(t)

		test.ErrorIs(t, impl.SetMany(t.Context(), map[string]*example{"a": {Name: "spot"}}), cache.ErrUnavailable)
		test.SliceLen(t, 0, client.EvalCalls())
	})
}

func Test_Cache_CodecFailures_Unit(T *testing.T) {
	T.Parallel()

	T.Run("Get treats a nil decode as a miss", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.codec = nilCodec{}

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.GetFunc = func(context.Context, string) *redis.StringCmd {
			cmd := redis.NewStringCmd(ctx)
			cmd.SetVal("whatever")
			return cmd
		}

		got, err := impl.Get(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.Nil(t, got)
	})

	T.Run("GetMany treats a nil decode as a miss", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.codec = nilCodec{}

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.MGetFunc = func(context.Context, ...string) *redis.SliceCmd {
			cmd := redis.NewSliceCmd(ctx)
			cmd.SetVal([]any{"whatever"})
			return cmd
		}

		out, err := impl.GetMany(ctx, []string{exampleKey})
		must.NoError(t, err)
		test.MapLen(t, 0, out)
	})

	T.Run("Set surfaces an encoding failure without touching the client", func(t *testing.T) {
		t.Parallel()

		impl, client, cb, _ := buildTestImpl(t)
		impl.codec = brokenCodec{}

		cb.CannotProceedFunc = func() bool { return false }

		test.Error(t, impl.Set(t.Context(), exampleKey, &example{Name: "spot"}))
		test.SliceLen(t, 0, client.SetCalls())
	})

	T.Run("SetMany fails the whole batch before any write", func(t *testing.T) {
		t.Parallel()

		impl, client, cb, _ := buildTestImpl(t)
		impl.codec = brokenCodec{}

		cb.CannotProceedFunc = func() bool { return false }

		test.Error(t, impl.SetMany(t.Context(), map[string]*example{"a": {Name: "spot"}}))
		test.SliceLen(t, 0, client.EvalCalls())
	})
}

func Test_Cache_CustomCodec_Unit(T *testing.T) {
	T.Parallel()

	// Option carries no T, so a codec for another type type-checks. Keeping the
	// gob default silently would be worse than failing: the cache would encode
	// correctly and the caller would never learn their codec was ignored.
	T.Run("NewRedisCache rejects a codec for a different type", func(t *testing.T) {
		t.Parallel()

		_, err := NewRedisCache[example](
			&Config{Addresses: []string{"localhost:6379"}},
			time.Minute,
			nil,
			WithCodec(cache.NewGobCodec[struct{ Other string }]()),
		)
		test.ErrorIs(t, err, ErrCodecTypeMismatch)
	})

	T.Run("NewRedisCache accepts a codec for its own type without a type argument", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](
			&Config{Addresses: []string{"localhost:6379"}},
			time.Minute,
			nil,
			WithCodec(nameCodec{}),
		)
		must.NoError(t, err)

		test.EqOp(t, any(nameCodec{}), any(c.codec))
	})

	T.Run("Set stores the codec's bytes and Get decodes them", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.codec = nameCodec{}

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetFunc = func(_ context.Context, _ string, value any, _ time.Duration) *redis.StatusCmd {
			s, isString := value.(string)
			must.True(t, isString)
			test.EqOp(t, "spot", s)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}
		client.GetFunc = func(_ context.Context, _ string) *redis.StringCmd {
			cmd := redis.NewStringCmd(ctx)
			cmd.SetVal("spot")
			return cmd
		}

		must.NoError(t, impl.Set(ctx, exampleKey, &example{Name: "spot"}))

		got, err := impl.Get(ctx, exampleKey)
		must.NoError(t, err)
		test.EqOp(t, "spot", got.Name)
	})

	T.Run("nil codec option is ignored, keeping the gob default", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		// The option records nothing for a nil codec, so the constructor never
		// overwrites the gob default it installed. Asserted on the options
		// struct because that is the only place the distinction exists.
		o := &options{}
		WithCodec[example](nil)(o)
		test.Nil(t, o.codec)

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		expected := gobEncodeExample(t, &example{Name: "spot"})
		client.SetFunc = func(_ context.Context, _ string, value any, _ time.Duration) *redis.StatusCmd {
			s, isString := value.(string)
			must.True(t, isString)
			test.EqOp(t, expected, s)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}

		must.NoError(t, impl.Set(ctx, exampleKey, &example{Name: "spot"}))
	})
}

func Test_Cache_Namespace_Unit(T *testing.T) {
	T.Parallel()

	T.Run("keys are namespaced on the wire and bare in results", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.SetFunc = func(_ context.Context, key string, _ any, _ time.Duration) *redis.StatusCmd {
			test.EqOp(t, "ns:"+exampleKey, key)
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("OK")
			return cmd
		}
		client.MGetFunc = func(_ context.Context, keys ...string) *redis.SliceCmd {
			must.SliceLen(t, 1, keys)
			test.EqOp(t, "ns:"+exampleKey, keys[0])
			cmd := redis.NewSliceCmd(ctx)
			cmd.SetVal([]any{gobEncodeExample(t, &example{Name: "spot"})})
			return cmd
		}

		must.NoError(t, impl.Set(ctx, exampleKey, &example{Name: "spot"}))

		out, err := impl.GetMany(ctx, []string{exampleKey})
		must.NoError(t, err)
		must.MapLen(t, 1, out)
		// The result map is keyed by the caller's bare key, not the stored one.
		must.NotNil(t, out[exampleKey])
		test.EqOp(t, "spot", out[exampleKey].Name)
	})
}

func Test_Cache_Deletion_Unit(T *testing.T) {
	T.Parallel()

	T.Run("DeleteMany issues one DEL with namespaced keys", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.DelFunc = func(_ context.Context, keys ...string) *redis.IntCmd {
			test.Eq(t, []string{"ns:a", "ns:b"}, keys)
			cmd := redis.NewIntCmd(ctx)
			cmd.SetVal(int64(len(keys)))
			return cmd
		}

		must.NoError(t, impl.DeleteMany(ctx, []string{"a", "b"}))
		test.SliceLen(t, 1, client.DelCalls())
	})

	T.Run("Flush without a namespace is refused", func(t *testing.T) {
		t.Parallel()

		impl, _, _, _ := buildTestImpl(t)

		test.ErrorIs(t, impl.Flush(t.Context()), cache.ErrNamespaceRequired)
	})

	T.Run("DeleteByPrefix with no namespace and empty prefix is refused", func(t *testing.T) {
		t.Parallel()

		impl, _, _, _ := buildTestImpl(t)

		test.ErrorIs(t, impl.DeleteByPrefix(t.Context(), ""), cache.ErrNamespaceRequired)
	})

	T.Run("Flush scans the namespace pattern and deletes what it finds", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		pages := [][]string{{"ns:a", "ns:b"}, {"ns:c"}}
		cursors := []uint64{7, 0}
		call := 0
		client.ScanFunc = func(_ context.Context, cursor uint64, match string, count int64) *redis.ScanCmd {
			test.EqOp(t, "ns:*", match)
			cmd := redis.NewScanCmd(ctx, nil)
			cmd.SetVal(pages[call], cursors[call])
			call++
			return cmd
		}

		var deleted []string
		client.DelFunc = func(_ context.Context, keys ...string) *redis.IntCmd {
			deleted = append(deleted, keys...)
			cmd := redis.NewIntCmd(ctx)
			cmd.SetVal(int64(len(keys)))
			return cmd
		}

		must.NoError(t, impl.Flush(ctx))
		test.Eq(t, []string{"ns:a", "ns:b", "ns:c"}, deleted)
		test.SliceLen(t, 2, client.ScanCalls())
	})

	T.Run("DeleteByPrefix escapes glob metacharacters in the prefix", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		client.ScanFunc = func(_ context.Context, _ uint64, match string, _ int64) *redis.ScanCmd {
			test.EqOp(t, `ns:area\[1\]:*`, match)
			cmd := redis.NewScanCmd(ctx, nil)
			cmd.SetVal([]string{}, 0)
			return cmd
		}

		must.NoError(t, impl.DeleteByPrefix(ctx, "area[1]:"))
	})
}

func Test_Cache_Deletion_Failures_Unit(T *testing.T) {
	T.Parallel()

	T.Run("DeleteMany with no keys never reaches the client", func(t *testing.T) {
		t.Parallel()

		impl, client, _, _ := buildTestImpl(t)

		must.NoError(t, impl.DeleteMany(t.Context(), nil))
		test.SliceLen(t, 0, client.DelCalls())
	})

	T.Run("DeleteMany reports a failed DEL and trips the breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)

		var failed int
		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() { failed++ }

		client.DelFunc = func(context.Context, ...string) *redis.IntCmd {
			cmd := redis.NewIntCmd(ctx)
			cmd.SetErr(errors.New("connection reset"))
			return cmd
		}

		test.Error(t, impl.DeleteMany(ctx, []string{"a"}))
		test.EqOp(t, 1, failed)
	})

	T.Run("DeleteByPrefix reports a failed SCAN and trips the breaker", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		var failed int
		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() { failed++ }

		client.ScanFunc = func(context.Context, uint64, string, int64) *redis.ScanCmd {
			cmd := redis.NewScanCmd(ctx, nil)
			cmd.SetErr(errors.New("connection reset"))
			return cmd
		}

		test.Error(t, impl.DeleteByPrefix(ctx, "p:"))
		test.EqOp(t, 1, failed)
	})

	T.Run("DeleteByPrefix reports a failed DEL mid-scan", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		client.ScanFunc = func(context.Context, uint64, string, int64) *redis.ScanCmd {
			cmd := redis.NewScanCmd(ctx, nil)
			// A non-zero cursor: the scan would continue if the DEL succeeded.
			cmd.SetVal([]string{"ns:a"}, 9)
			return cmd
		}
		client.DelFunc = func(context.Context, ...string) *redis.IntCmd {
			cmd := redis.NewIntCmd(ctx)
			cmd.SetErr(errors.New("connection reset"))
			return cmd
		}

		test.Error(t, impl.DeleteByPrefix(ctx, "p:"))
		// The failure stopped the cursor rather than looping forever.
		test.SliceLen(t, 1, client.ScanCalls())
	})

	T.Run("a cluster client scans every master", func(t *testing.T) {
		t.Parallel()

		// A real ClusterClient is the only way into the ForEachMaster branch —
		// the type is asserted, not interface-dispatched. Pointing it at a
		// closed port makes the fan-out fail fast, which is enough to prove the
		// branch was taken.
		impl, _, cb, _ := buildTestImpl(t)
		impl.namespace = "ns:"
		impl.isCluster = true
		impl.client = redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:        []string{"127.0.0.1:1"},
			DialTimeout:  100 * time.Millisecond,
			ReadTimeout:  100 * time.Millisecond,
			WriteTimeout: 100 * time.Millisecond,
			MaxRedirects: 1,
		})

		cb.CannotProceedFunc = func() bool { return false }
		cb.FailedFunc = func() {}

		test.Error(t, impl.DeleteByPrefix(t.Context(), "p:"))
	})
}

func TestWithScanPageSize(T *testing.T) {
	T.Parallel()

	// scanCount drives a one-page prefix deletion and reports the COUNT the
	// cache actually handed to SCAN, so these assert the option reaches the
	// wire rather than just landing in a struct field.
	scanCount := func(t *testing.T, impl *Cache[example], client *redisClientMock, cb *circuitbreakingmock.CircuitBreakerMock) int64 {
		t.Helper()

		ctx := t.Context()
		impl.namespace = "ns:"
		cb.CannotProceedFunc = func() bool { return false }
		cb.SucceededFunc = func() {}

		var got int64
		client.ScanFunc = func(_ context.Context, _ uint64, _ string, count int64) *redis.ScanCmd {
			got = count
			cmd := redis.NewScanCmd(ctx, nil)
			cmd.SetVal([]string{}, 0)
			return cmd
		}

		must.NoError(t, impl.DeleteByPrefix(ctx, "p:"))

		return got
	}

	T.Run("the configured size reaches SCAN", func(t *testing.T) {
		t.Parallel()

		impl, client, cb, _ := buildTestImpl(t)
		impl.scanPageSize = 25

		test.EqOp(t, int64(25), scanCount(t, impl, client, cb))
	})

	T.Run("the default reaches SCAN when the option is not supplied", func(t *testing.T) {
		t.Parallel()

		impl, client, cb, _ := buildTestImpl(t)

		test.EqOp(t, int64(defaultScanPageSize), scanCount(t, impl, client, cb))
	})

	T.Run("a non-positive size is ignored", func(t *testing.T) {
		t.Parallel()

		// Asserted against an options struct seeded the way NewRedisCache seeds
		// it, since "ignored" means the option must not overwrite the default.
		for _, size := range []int64{0, -1} {
			o := &options{scanPageSize: defaultScanPageSize}
			WithScanPageSize(size)(o)

			test.EqOp(t, int64(defaultScanPageSize), o.scanPageSize)
		}
	})

	T.Run("NewRedisCache applies the option", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](
			&Config{Addresses: []string{"localhost:6379"}},
			time.Minute,
			nil,
			WithScanPageSize(64),
		)
		must.NoError(t, err)

		test.EqOp(t, int64(64), c.scanPageSize)
	})
}

func TestWithLogger(T *testing.T) {
	T.Parallel()

	T.Run("sets the logger", func(t *testing.T) {
		t.Parallel()

		o := &options{}
		WithLogger(loggingnoop.NewLogger())(o)

		test.NotNil(t, o.logger)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := &options{logger: loggingnoop.NewLogger()}
		WithLogger(nil)(o)

		test.Nil(t, o.logger)
	})

	T.Run("NewRedisCache applies the option", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](
			&Config{Addresses: []string{"localhost:6379"}},
			time.Minute,
			nil,
			WithLogger(loggingnoop.NewLogger()),
		)
		must.NoError(t, err)

		test.NotNil(t, c.logger)
	})
}

func TestWithTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		o := &options{}
		WithTracerProvider(tracingnoop.NewTracerProvider())(o)

		test.NotNil(t, o.tracerProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := &options{tracerProvider: tracingnoop.NewTracerProvider()}
		WithTracerProvider(nil)(o)

		test.Nil(t, o.tracerProvider)
	})

	T.Run("NewRedisCache applies the option", func(t *testing.T) {
		t.Parallel()

		c, err := NewRedisCache[example](
			&Config{Addresses: []string{"localhost:6379"}},
			time.Minute,
			nil,
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		test.NotNil(t, c.tracerProvider)
	})
}

func TestWithMetricsProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the metrics provider", func(t *testing.T) {
		t.Parallel()

		o := &options{}
		WithMetricsProvider(metricsnoop.NewMetricsProvider())(o)

		test.NotNil(t, o.metricsProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := &options{metricsProvider: metricsnoop.NewMetricsProvider()}
		WithMetricsProvider(nil)(o)

		test.Nil(t, o.metricsProvider)
	})
}

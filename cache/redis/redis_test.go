package redis

import (
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/cache"
	mockcircuitbreaking "github.com/primandproper/platform-go/circuitbreaking/mock"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/testutils/containers/redistest"

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

func buildTestImpl(t *testing.T) (*redisCacheImpl[example], *redisClientMock, *mockcircuitbreaking.CircuitBreakerMock) {
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
	cb := &mockcircuitbreaking.CircuitBreakerMock{}

	return &redisCacheImpl[example]{
		logger:           loggingnoop.NewLogger(),
		tracer:           tracing.NewNamedTracer(nil, "test"),
		cacheHitCounter:  hitCounter,
		cacheMissCounter: missCounter,
		cacheSetCounter:  setCounter,
		cacheDelCounter:  delCounter,
		cacheErrCounter:  errCounter,
		latencyHist:      latencyHist,
		client:           client,
		circuitBreaker:   cb,
		expiration:       time.Minute,
	}, client, cb
}

// counterResult bundles the values a mocked NewInt64Counter call returns.
type counterResult struct {
	counter metrics.Int64Counter
	err     error
}

// newCounterProviderMock returns a metrics.Provider mock whose NewInt64Counter
// implementation looks up the result keyed on the counter name. Unknown names
// fail the test.
func newCounterProviderMock(t *testing.T, results map[string]counterResult) *mockmetrics.ProviderMock {
	t.Helper()
	return &mockmetrics.ProviderMock{
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
		QueueAddresses: []string{redistest.Address(t, container)},
	}
}

func TestNewRedisCache(T *testing.T) {
	T.Parallel()

	okCounter := func() metrics.Int64Counter { return metrics.Int64CounterForTest(T, "x") }

	T.Run("with single address", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, nil, nil)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with multiple addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379", "localhost:6380"}}

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, nil, nil)
		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("with error creating cache hit counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache miss counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":   {counter: okCounter()},
			name + "_cache_misses": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache set counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":   {counter: okCounter()},
			name + "_cache_misses": {counter: okCounter()},
			name + "_cache_sets":   {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache delete counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":    {counter: okCounter()},
			name + "_cache_misses":  {counter: okCounter()},
			name + "_cache_sets":    {counter: okCounter()},
			name + "_cache_deletes": {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 4, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating cache error counter", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		mp := newCounterProviderMock(t, map[string]counterResult{
			name + "_cache_hits":    {counter: okCounter()},
			name + "_cache_misses":  {counter: okCounter()},
			name + "_cache_sets":    {counter: okCounter()},
			name + "_cache_deletes": {counter: okCounter()},
			name + "_cache_errors":  {counter: okCounter(), err: errors.New("counter error")},
		})

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{QueueAddresses: []string{"localhost:6379"}}

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(metricName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_cache_latency_ms", metricName)
				return h, errors.New("histogram error")
			},
		}

		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, mp, nil)
		test.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 5, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func Test_redisCacheImpl_Get(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil, nil, nil, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))

		actual, getErr := c.Get(ctx, exampleKey)
		test.Eq(t, exampleContent, actual)
		test.NoError(t, getErr)
	})
}

func Test_redisCacheImpl_Get_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, _, cb := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		actual, err := impl.Get(ctx, exampleKey)
		test.ErrorIs(t, err, cache.ErrNotFound)
		test.Nil(t, actual)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
	})

	T.Run("with decode error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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

func Test_redisCacheImpl_Set(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil, nil, nil, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))
	})
}

func Test_redisCacheImpl_Set_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
		impl, _, cb := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		err := impl.Set(ctx, exampleKey, &example{Name: t.Name()})
		test.NoError(t, err)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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

func Test_redisCacheImpl_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, 0, nil, nil, nil, nil)
		must.NoError(t, err)

		exampleContent := &example{Name: t.Name()}
		test.NoError(t, c.Set(ctx, exampleKey, exampleContent))

		test.NoError(t, c.Delete(ctx, exampleKey))
	})
}

func Test_redisCacheImpl_Delete_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
		impl, _, cb := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		err := impl.Delete(ctx, exampleKey)
		test.NoError(t, err)

		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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

func Test_redisCacheImpl_Ping_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, _ := buildTestImpl(t)

		client.PingFunc = func(_ context.Context) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetVal("PONG")
			return cmd
		}

		test.NoError(t, impl.Ping(ctx))
		test.SliceLen(t, 1, client.PingCalls())
	})

	T.Run("with error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, _ := buildTestImpl(t)

		client.PingFunc = func(_ context.Context) *redis.StatusCmd {
			cmd := redis.NewStatusCmd(ctx)
			cmd.SetErr(errors.New("connection refused"))
			return cmd
		}

		test.Error(t, impl.Ping(ctx))
		test.SliceLen(t, 1, client.PingCalls())
	})
}

func Test_redisCacheImpl_GetMany_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard with hit and miss", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
	})

	T.Run("empty keys short-circuits", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, _ := buildTestImpl(t)

		out, err := impl.GetMany(ctx, nil)
		test.NoError(t, err)
		test.MapLen(t, 0, out)
		test.SliceLen(t, 0, client.MGetCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		out, err := impl.GetMany(ctx, []string{"a", "b"})
		test.NoError(t, err)
		test.MapLen(t, 0, out)
		test.SliceLen(t, 0, client.MGetCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
	})

	T.Run("with decode error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
		impl, client, cb := buildTestImpl(t)
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

func Test_redisCacheImpl_SetMany_Unit(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
		impl, client, _ := buildTestImpl(t)

		test.NoError(t, impl.SetMany(ctx, nil))
		test.SliceLen(t, 0, client.EvalCalls())
	})

	T.Run("when circuit breaker cannot proceed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

		cb.CannotProceedFunc = func() bool { return true }

		test.NoError(t, impl.SetMany(ctx, map[string]*example{"a": {Name: "a"}}))
		test.SliceLen(t, 0, client.EvalCalls())
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with redis error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		impl, client, cb := buildTestImpl(t)

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
		impl, client, cb := buildTestImpl(t)
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

func Test_redisCacheImpl_SetMany_GetMany(T *testing.T) {
	T.Parallel()

	T.Run("round trip against a real redis", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := buildContainerBackedRedisConfig(t)
		c, err := NewRedisCache[example](cfg, time.Minute, nil, nil, nil, nil)
		must.NoError(t, err)

		bc, ok := c.(cache.BatchCache[example])
		must.True(t, ok)

		items := map[string]*example{
			"k1": {Name: "one"},
			"k2": {Name: "two"},
		}
		test.NoError(t, bc.SetMany(ctx, items))

		out, getErr := bc.GetMany(ctx, []string{"k1", "k2", "k3"})
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
			QueueAddresses: []string{"localhost:6379"},
			Username:       "user",
			Password:       "pass",
		}

		c := buildRedisClient(cfg)
		test.NotNil(t, c)
	})

	T.Run("with multiple addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			QueueAddresses: []string{"localhost:6379", "localhost:6380"},
			Username:       "user",
			Password:       "pass",
		}

		c := buildRedisClient(cfg)
		test.NotNil(t, c)
	})

	T.Run("with no addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			QueueAddresses: []string{},
		}

		c := buildRedisClient(cfg)
		test.Nil(t, c)
	})
}

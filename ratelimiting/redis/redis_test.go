package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v6/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v6/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"

	"github.com/redis/go-redis/v9"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type evalCall struct {
	ctx    context.Context
	script string
	keys   []string
	args   []any
}

type mockRedisClient struct {
	evalFunc   func(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd
	closeFunc  func() error
	evalCalls  []evalCall
	closeCalls int
}

func (m *mockRedisClient) Eval(ctx context.Context, script string, keys []string, args ...any) *redis.Cmd {
	m.evalCalls = append(m.evalCalls, evalCall{ctx: ctx, script: script, keys: keys, args: args})
	return m.evalFunc(ctx, script, keys, args...)
}

func (m *mockRedisClient) Close() error {
	m.closeCalls++
	return m.closeFunc()
}

func buildTestRateLimiter(t *testing.T) (*rateLimiter, *mockRedisClient) {
	t.Helper()

	client := &mockRedisClient{}
	mp := metricsnoop.NewMetricsProvider()

	allowedCounter, err := mp.NewInt64Counter(redisName + "_allowed")
	must.NoError(t, err)

	rejectedCounter, err := mp.NewInt64Counter(redisName + "_rejected")
	must.NoError(t, err)

	errorCounter, err := mp.NewInt64Counter(redisName + "_errors")
	must.NoError(t, err)

	return &rateLimiter{
		client:          client,
		requestsPerSec:  10,
		burstSize:       20,
		allowedCounter:  allowedCounter,
		rejectedCounter: rejectedCounter,
		errorCounter:    errorCounter,
	}, client
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{
			Addresses: []string{"localhost:6379"},
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with empty addresses", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{
			Addresses: []string{},
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with nil addresses", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		cfg := &Config{}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestNewRedisRateLimiter(T *testing.T) {
	T.Parallel()

	T.Run("with no addresses", func(t *testing.T) {
		t.Parallel()

		rl, err := NewRedisRateLimiter(Config{}, nil, 10, 20)
		test.Error(t, err)
		test.Nil(t, rl)
	})

	T.Run("with single address", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379"},
			Username:  "user",
			Password:  "pass",
		}

		rl, err := NewRedisRateLimiter(cfg, nil, 10, 20)
		test.NoError(t, err)
		test.NotNil(t, rl)
	})

	T.Run("with multiple addresses", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379", "localhost:6380"},
			Username:  "user",
			Password:  "pass",
		}

		rl, err := NewRedisRateLimiter(cfg, nil, 10, 20)
		test.NoError(t, err)
		test.NotNil(t, rl)
	})

	T.Run("with error creating allowed counter", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379"},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, redisName+"_allowed", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("counter error")
			},
		}

		rl, err := NewRedisRateLimiter(cfg, mp, 10, 20)
		test.Error(t, err)
		test.Nil(t, rl)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating rejected counter", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379"},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case redisName + "_allowed":
					return metrics.Int64CounterForTest(t, "x"), nil
				case redisName + "_rejected":
					return metrics.Int64CounterForTest(t, "x"), errors.New("counter error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		rl, err := NewRedisRateLimiter(cfg, mp, 10, 20)
		test.Error(t, err)
		test.Nil(t, rl)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379"},
		}

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case redisName + "_allowed", redisName + "_rejected":
					return metrics.Int64CounterForTest(t, "x"), nil
				case redisName + "_errors":
					return metrics.Int64CounterForTest(t, "x"), errors.New("counter error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		rl, err := NewRedisRateLimiter(cfg, mp, 10, 20)
		test.Error(t, err)
		test.Nil(t, rl)

		test.SliceLen(t, 3, mp.NewInt64CounterCalls())
	})
}

func Test_rateLimiter_Allow(T *testing.T) {
	T.Parallel()

	T.Run("allowed", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		rl, client := buildTestRateLimiter(t)

		cmd := redis.NewCmd(ctx)
		cmd.SetVal(int64(1))
		client.evalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd { return cmd }

		allowed, err := rl.Allow(ctx, "test-key")
		test.NoError(t, err)
		test.True(t, allowed)

		must.SliceLen(t, 1, client.evalCalls)
		test.EqOp(t, slidingWindowScript, client.evalCalls[0].script)
	})

	T.Run("rejected", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		rl, client := buildTestRateLimiter(t)

		cmd := redis.NewCmd(ctx)
		cmd.SetVal(int64(0))
		client.evalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd { return cmd }

		allowed, err := rl.Allow(ctx, "test-key")
		test.NoError(t, err)
		test.False(t, allowed)

		must.SliceLen(t, 1, client.evalCalls)
	})

	T.Run("passes burst as the window limit and does not truncate a fractional rate to zero", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		client := &mockRedisClient{}
		mp := metricsnoop.NewMetricsProvider()
		allowed, err := mp.NewInt64Counter(redisName + "_a")
		must.NoError(t, err)
		rejected, err := mp.NewInt64Counter(redisName + "_r")
		must.NoError(t, err)
		errc, err := mp.NewInt64Counter(redisName + "_e")
		must.NoError(t, err)

		// 0.5 rps with a burst of 3: the old int64(0.5)=0 limit rejected everything.
		rl := &rateLimiter{client: client, requestsPerSec: 0.5, burstSize: 3, allowedCounter: allowed, rejectedCounter: rejected, errorCounter: errc}

		cmd := redis.NewCmd(ctx)
		cmd.SetVal(int64(1))
		client.evalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd { return cmd }

		ok, err := rl.Allow(ctx, "k")
		test.NoError(t, err)
		test.True(t, ok)

		must.SliceLen(t, 1, client.evalCalls)
		args := client.evalCalls[0].args
		// args: now, windowMS, limit, member
		must.SliceLen(t, 4, args)
		test.EqOp(t, int64(3), args[2].(int64))    // limit == burstSize, not floored to 0
		test.EqOp(t, int64(6000), args[1].(int64)) // window == burst/rate = 3/0.5 = 6s
	})

	T.Run("with eval error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		rl, client := buildTestRateLimiter(t)

		cmd := redis.NewCmd(ctx)
		cmd.SetErr(errors.New("redis error"))
		client.evalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd { return cmd }

		allowed, err := rl.Allow(ctx, "test-key")
		test.Error(t, err)
		test.False(t, allowed)

		must.SliceLen(t, 1, client.evalCalls)
	})

	// Regression: the ZADD member must be unique per request. Keying solely on
	// the millisecond timestamp collapses every request within the same
	// millisecond into one ZSET entry (ZADD only updates the score of an
	// existing member), which silently bypasses the limit under load. A tight
	// loop spans far fewer distinct milliseconds than iterations, so the buggy
	// version produces duplicate members here and this test fails.
	T.Run("emits a unique ZADD member per request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		rl, client := buildTestRateLimiter(t)

		cmd := redis.NewCmd(ctx)
		cmd.SetVal(int64(1))
		client.evalFunc = func(_ context.Context, _ string, _ []string, _ ...any) *redis.Cmd { return cmd }

		const calls = 1000
		for range calls {
			allowed, err := rl.Allow(ctx, "test-key")
			test.NoError(t, err)
			test.True(t, allowed)
		}

		must.SliceLen(t, calls, client.evalCalls)

		members := make(map[string]struct{}, calls)
		for _, c := range client.evalCalls {
			// args are: now, windowMS, limit, member
			must.SliceLen(t, 4, c.args)
			member, ok := c.args[3].(string)
			must.True(t, ok)
			members[member] = struct{}{}
		}

		test.MapLen(t, calls, members)
	})
}

func Test_rateLimiter_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		rl, client := buildTestRateLimiter(t)
		client.closeFunc = func() error { return nil }

		err := rl.Close()
		test.NoError(t, err)
		test.EqOp(t, 1, client.closeCalls)
	})

	T.Run("with close error", func(t *testing.T) {
		t.Parallel()

		rl, client := buildTestRateLimiter(t)
		client.closeFunc = func() error { return errors.New("close failed") }

		err := rl.Close()
		test.Error(t, err)
		test.EqOp(t, 1, client.closeCalls)
	})
}

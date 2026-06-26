package redis

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"

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

	T.Run("with single address", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			Addresses: []string{"localhost:6379"},
			Username:  "user",
			Password:  "pass",
		}

		rl, err := NewRedisRateLimiter(cfg, nil, 10)
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

		rl, err := NewRedisRateLimiter(cfg, nil, 10)
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

		rl, err := NewRedisRateLimiter(cfg, mp, 10)
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

		rl, err := NewRedisRateLimiter(cfg, mp, 10)
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

		rl, err := NewRedisRateLimiter(cfg, mp, 10)
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

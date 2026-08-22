package distributedlockcfg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/distributedlock"
	pglock "github.com/primandproper/platform-go/v13/distributedlock/postgres"
	redislock "github.com/primandproper/platform-go/v13/distributedlock/redis"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// stubDBClient is a minimal database.Client for constructing a postgres locker
// without requiring a real database connection. The locker constructor stores
// the client but does not use it until a lock is acquired.
type stubDBClient struct{}

func (c *stubDBClient) WriteDB() *sql.DB                  { return nil }
func (c *stubDBClient) ReadDB() *sql.DB                   { return nil }
func (c *stubDBClient) Reader() database.SQLQueryExecutor { return nil }
func (c *stubDBClient) Writer() database.SQLQueryExecutor { return nil }
func (c *stubDBClient) Dialect() dialect.Dialect          { return dialect.Postgres }
func (c *stubDBClient) Close() error                      { return nil }
func (c *stubDBClient) CurrentTime() time.Time            { return time.Now() }
func (c *stubDBClient) RollbackTransaction(_ context.Context, _ database.SQLQueryExecutorAndTransactionManager) {
}

func (c *stubDBClient) WithTransaction(_ context.Context, _ func(tx database.SQLQueryExecutor) error) error {
	return nil
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: RedisProvider,
			Redis: &redislock.Config{
				Addresses: []string{"localhost:6379"},
				KeyPrefix: "lock:",
			},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("postgres provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Provider: PostgresProvider,
			Postgres: &pglock.Config{},
		}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: MemoryProvider}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("noop provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: NoopProvider}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("redis without config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: RedisProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("postgres without config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: PostgresProvider}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: "made-up"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("empty provider is invalid", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewLocker(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		_, err := NewLocker(
			t.Context(),
			nil,
			nil,
		)
		test.ErrorIs(t, err, distributedlock.ErrNilConfig)
	})

	T.Run("memory provider returns a working locker", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(
			t.Context(),
			&Config{Provider: MemoryProvider},
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
		lock, err := l.Acquire(t.Context(), "k", time.Second)
		must.NoError(t, err)
		must.NoError(t, lock.Release(t.Context()))
	})

	T.Run("noop provider", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(
			t.Context(),
			&Config{Provider: NoopProvider},
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("the noop provider returns the noop locker", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(
			t.Context(),
			&Config{Provider: NoopProvider},
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	// A locker whose Acquire always succeeds has to be asked for by name: an
	// unset or typo'd provider silently removes mutual exclusion everywhere.
	for _, provider := range []string{"unknown", "", "   "} {
		T.Run("rejects provider "+strconv.Quote(provider), func(t *testing.T) {
			t.Parallel()
			l, err := NewLocker(
				t.Context(),
				&Config{Provider: provider},
				nil,
			)
			test.Error(t, err)
			test.Nil(t, l)
		})
	}

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(
			t.Context(),
			&Config{
				Provider: RedisProvider,
				Redis: &redislock.Config{
					Addresses: []string{"localhost:6379"},
					KeyPrefix: "lock:",
				},
			},
			nil,
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("postgres provider", func(t *testing.T) {
		t.Parallel()
		l, err := NewLocker(
			t.Context(),
			&Config{
				Provider: PostgresProvider,
				Postgres: &pglock.Config{},
			},
			&stubDBClient{},
		)
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: MemoryProvider,
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "dlock-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, circuitbreakingcfg.TrippedCounterName, counterName)
				return &metricsmock.Int64CounterMock{}, fmt.Errorf("counter init failure")
			},
		}

		l, err := NewLocker(
			t.Context(),
			cfg,
			nil,
			WithMetricsProvider(mp),
		)
		must.Error(t, err)
		test.Nil(t, l)
		test.StrContains(t, err.Error(), "distributedlock circuit breaker")

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestNewScopedLocker(T *testing.T) {
	T.Parallel()

	newScoped := func(t *testing.T, cfg *Config, db database.Client) (distributedlock.ScopedLocker, error) {
		t.Helper()

		return NewScopedLocker(t.Context(), cfg, db)
	}

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := newScoped(t, nil, nil)
		test.ErrorIs(t, err, distributedlock.ErrNilConfig)
	})

	T.Run("memory provider returns a working scoped locker", func(t *testing.T) {
		t.Parallel()

		s, err := newScoped(t, &Config{Provider: MemoryProvider}, nil)
		must.NoError(t, err)
		must.NotNil(t, s)

		ran := false
		must.NoError(t, s.WithLock(t.Context(), "chore", func(context.Context) error {
			ran = true
			return nil
		}))
		test.True(t, ran)
	})

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()

		s, err := newScoped(t, &Config{
			Provider: RedisProvider,
			Redis: &redislock.Config{
				Addresses: []string{"localhost:6379"},
				KeyPrefix: "lock:",
			},
		}, nil)
		must.NoError(t, err)
		must.NotNil(t, s)
	})

	T.Run("postgres provider gets the native transaction-scoped implementation", func(t *testing.T) {
		t.Parallel()

		s, err := newScoped(t, &Config{
			Provider: PostgresProvider,
			Postgres: &pglock.Config{},
		}, &stubDBClient{})
		must.NoError(t, err)
		must.NotNil(t, s)
	})

	T.Run("postgres provider without a database client", func(t *testing.T) {
		t.Parallel()

		_, err := newScoped(t, &Config{
			Provider: PostgresProvider,
			Postgres: &pglock.Config{},
		}, nil)
		test.ErrorIs(t, err, distributedlock.ErrNilDatabaseClient)
	})

	T.Run("redis provider with no config fails through NewLocker", func(t *testing.T) {
		t.Parallel()

		_, err := newScoped(t, &Config{Provider: RedisProvider}, nil)
		test.Error(t, err)
	})

	T.Run("the noop provider still runs fn", func(t *testing.T) {
		t.Parallel()

		s, err := newScoped(t, &Config{Provider: NoopProvider}, nil)
		must.NoError(t, err)
		must.NotNil(t, s)

		// Selected deliberately, the noop locker still runs fn, so a deployment
		// with nothing to coordinate with is not silently skipping work.
		ran := false
		must.NoError(t, s.WithLock(t.Context(), "chore", func(context.Context) error {
			ran = true
			return nil
		}))
		test.True(t, ran)
	})

	// Mutual exclusion vanishing because of a typo is the failure this guards.
	for _, provider := range []string{"unknown", "", "   "} {
		T.Run("rejects provider "+strconv.Quote(provider), func(t *testing.T) {
			t.Parallel()

			s, err := newScoped(t, &Config{Provider: provider}, nil)
			test.Error(t, err)
			test.Nil(t, s)
		})
	}

	T.Run("circuit breaker init failure on the postgres path", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: PostgresProvider,
			Postgres: &pglock.Config{},
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "dlock-scoped-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, circuitbreakingcfg.TrippedCounterName, counterName)
				return &metricsmock.Int64CounterMock{}, fmt.Errorf("counter init failure")
			},
		}

		s, err := NewScopedLocker(t.Context(), cfg, &stubDBClient{}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, s)
		test.StrContains(t, err.Error(), "distributedlock circuit breaker")
	})
}

// TestNewLocker_nilInterfaceOnError guards the narrowing from a provider's own
// concrete type back to distributedlock.Locker. Returning a constructor's result
// straight through — `return memory.NewLocker(...)` — converts a nil
// *memory.Locker into a non-nil distributedlock.Locker, so a caller that checks
// the returned interface against nil gets a value that panics on first use. The
// error is correct either way, which is what makes this invisible without a
// test.
//
// The assertion has to be `l == nil` rather than test.Nil: test.Nil falls back
// to reflect.Value.IsNil for pointer kinds, which reports a nil pointer boxed in
// a non-nil interface as nil, and so passes against the very bug under test.
func TestNewLocker_nilInterfaceOnError(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		// The circuit breaker is built before the provider and registers a
		// counter of its own, so failing every counter would stop ahead of the
		// locker at this package's own explicit `return nil, err`. Only the
		// locker's counters fail, which puts the failure inside memory.NewLocker
		// where the narrowing happens.
		noop := metrics.EnsureMetricsProvider(nil)
		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if strings.Contains(name, "circuit_breaker") {
					return noop.NewInt64Counter(name, opts...)
				}

				return nil, errors.New("counter init failure")
			},
		}

		l, err := NewLocker(t.Context(), &Config{Provider: MemoryProvider}, nil, WithMetricsProvider(mp))
		must.Error(t, err)
		test.True(t, l == nil, test.Sprintf("expected a nil distributedlock.Locker, got a non-nil interface holding %T", l))
	})
}

package cachecfg

import (
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/cache/redis"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

type example struct {
	Name string `json:"name"`
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderMemory,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("redis provider with config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{Addresses: []string{"localhost:6379"}},
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("redis provider missing config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderRedis}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "vault"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("bounded memory provider with a valid eviction policy", func(t *testing.T) {
		t.Parallel()

		for _, policy := range []string{"lru", "least_recently_used", "fifo", "oldest_written"} {
			cfg := &Config{Provider: ProviderMemory, MaxEntries: 1000, EvictionPolicy: policy}
			test.NoError(t, cfg.ValidateWithContext(t.Context()))
		}
	})

	T.Run("bounded memory provider with an unknown eviction policy", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMemory, MaxEntries: 1000, EvictionPolicy: "random"}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("an unbounded cache is not held to its eviction policy", func(t *testing.T) {
		t.Parallel()

		// The field carries a default that a cache with no bound never reads,
		// so leaving it empty — or wrong — cannot fail a configuration that
		// does not use it.
		cfg := &Config{Provider: ProviderMemory, EvictionPolicy: "random"}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("the redis provider is not held to the memory bound's policy", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:       ProviderRedis,
			Redis:          &redis.Config{Addresses: []string{"localhost:6379"}},
			MaxEntries:     1000,
			EvictionPolicy: "random",
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewCache(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		c, err := NewCache[example](
			t.Context(),
			&Config{
				Provider: ProviderMemory,
			},
		)

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{Addresses: []string{"localhost:6379"}},
		}
		cfg.CircuitBreaker.Name = "cache-breaker"

		c, err := NewCache[example](
			t.Context(),
			cfg,
		)

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider with cluster addresses", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{Addresses: []string{"localhost:6379", "localhost:6380"}},
		}
		cfg.CircuitBreaker.Name = "cache-breaker-cluster"

		c, err := NewCache[example](
			t.Context(),
			cfg,
		)

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("redis provider with circuit breaker error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{Addresses: []string{"localhost:6379"}},
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "redis-cache-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, circuitbreakingcfg.TrippedCounterName, name)
				return nil, errors.New("counter init failure")
			},
		}

		c, err := NewCache[example](
			t.Context(),
			cfg,
			WithMetricsProvider(mp),
		)

		must.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := NewCache[example](t.Context(), &Config{})

		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})

	T.Run("bounded memory provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		c, err := NewCache[example](ctx, &Config{
			Provider:       ProviderMemory,
			MaxEntries:     2,
			EvictionPolicy: "fifo",
		})
		must.NoError(t, err)
		must.NotNil(t, c)

		for _, key := range []string{"a", "b", "c"} {
			must.NoError(t, c.Set(ctx, key, &example{Name: key}))
		}

		// The bound reached the provider: the oldest write is gone.
		_, err = c.Get(ctx, "a")
		test.ErrorIs(t, err, cache.ErrNotFound)

		got, err := c.Get(ctx, "c")
		must.NoError(t, err)
		test.EqOp(t, "c", got.Name)
	})

	T.Run("memory provider with an unknown eviction policy", func(t *testing.T) {
		t.Parallel()

		_, err := NewCache[example](t.Context(), &Config{
			Provider:       ProviderMemory,
			MaxEntries:     10,
			EvictionPolicy: "random",
		})

		test.ErrorIs(t, err, memory.ErrUnknownEvictionPolicy)
	})

	T.Run("an unbounded memory provider ignores the eviction policy", func(t *testing.T) {
		t.Parallel()

		c, err := NewCache[example](t.Context(), &Config{
			Provider:       ProviderMemory,
			EvictionPolicy: "random",
		})

		must.NoError(t, err)
		test.NotNil(t, c)
	})
}

func TestNewCache_observabilityOptions(T *testing.T) {
	T.Parallel()

	memoryConfig := func() *Config { return &Config{Provider: ProviderMemory} }

	// breakerConfig selects the redis provider, whose circuit breaker registers
	// counters on whatever metrics provider it was handed. Failing that
	// registration is the cheapest way to observe which provider actually
	// reached the components being built.
	breakerConfig := func() *Config {
		return &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{Addresses: []string{"localhost:6379"}},
			CircuitBreaker: circuitbreakingcfg.Config{
				Name:                   "pillars-cache-breaker",
				ErrorRate:              50,
				MinimumSampleThreshold: 10,
			},
		}
	}

	failingProvider := func() *metricsmock.ProviderMock {
		return &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errors.New("counter init failure")
			},
		}
	}

	T.Run("builds with no observability at all", func(t *testing.T) {
		t.Parallel()

		// The reason the options exist: a caller that wants none of the three
		// names none of them, rather than naming three noops.
		c, err := NewCache[example](t.Context(), memoryConfig())

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("WithPillars supplies the metrics provider", func(t *testing.T) {
		t.Parallel()

		mp := failingProvider()

		c, err := NewCache[example](t.Context(), breakerConfig(), WithPillars(&observability.Pillars{
			MetricsProvider: mp,
		}))

		must.Error(t, err)
		test.Nil(t, c)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		c, err := NewCache[example](t.Context(), memoryConfig(), WithPillars(nil))

		must.NoError(t, err)
		test.NotNil(t, c)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		mp := failingProvider()

		// Options apply in order, so the later nil wins: the breaker's counters
		// go to the noop instead of this mock, and construction succeeds rather
		// than failing on its error.
		c, err := NewCache[example](t.Context(), breakerConfig(),
			WithPillars(&observability.Pillars{MetricsProvider: mp}),
			WithMetricsProvider(nil),
		)

		must.NoError(t, err)
		test.NotNil(t, c)
		test.SliceEmpty(t, mp.NewInt64CounterCalls())
	})
}

// TestNewCache_nilInterfaceOnError guards the narrowing from a provider's own
// concrete type back to cache.Cache. Returning a constructor's result straight
// through — `return memory.NewInMemoryCache[T](...)` — converts a nil
// *memory.Cache[T] into a non-nil cache.Cache[T], so a caller that checks the
// returned interface against nil gets a value that panics on first use. The
// error is correct either way, which is what makes this invisible without a
// test.
//
// The assertion has to be `c == nil` rather than test.Nil: test.Nil falls back
// to reflect.Value.IsNil for pointer kinds, which reports a nil pointer boxed in
// a non-nil interface as nil, and so passes against the very bug under test.
func TestNewCache_nilInterfaceOnError(T *testing.T) {
	T.Parallel()

	T.Run("memory provider", func(t *testing.T) {
		t.Parallel()

		// A failing counter registration is the only way to fail the memory
		// constructor itself from here: an unparseable eviction policy is caught
		// by this package before the constructor is ever reached, so it would
		// exercise the wrong return.
		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errors.New("counter init failure")
			},
		}

		c, err := NewCache[example](t.Context(), &Config{Provider: ProviderMemory}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.True(t, c == nil, test.Sprintf("expected a nil cache.Cache, got a non-nil interface holding %T", c))
	})

	T.Run("redis provider", func(t *testing.T) {
		t.Parallel()

		// No addresses fails inside the redis constructor.
		c, err := NewCache[example](t.Context(), &Config{
			Provider: ProviderRedis,
			Redis:    &redis.Config{},
		})
		must.Error(t, err)
		test.True(t, c == nil, test.Sprintf("expected a nil cache.Cache, got a non-nil interface holding %T", c))
	})
}

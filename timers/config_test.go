package timers

import (
	"math"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func validConfig() *Config {
	return &Config{Name: "trials"}
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills the unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultRetention, cfg.Retention)
		test.EqOp(t, DefaultMaxAttempts, cfg.MaxAttempts)
		test.EqOp(t, DefaultMaxClaimBatch, cfg.MaxClaimBatch)
		test.EqOp(t, DefaultReapBatchSize, cfg.ReapBatchSize)
		test.EqOp(t, uint(DefaultWriteAttempts), cfg.WriteAttempts)
		test.EqOp(t, DefaultMinWakeInterval, cfg.MinWakeInterval)
		test.EqOp(t, "scheduled_timers", cfg.resolvedTable())
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Retention = time.Hour
		cfg.MaxClaimBatch = 7
		cfg.EnsureDefaults()

		test.EqOp(t, time.Hour, cfg.Retention)
		test.EqOp(t, 7, cfg.MaxClaimBatch)
	})

	T.Run("resolves the table under a namespace", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.TablePrefix = "app"
		cfg.EnsureDefaults()

		test.EqOp(t, "app_scheduled_timers", cfg.resolvedTable())
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("requires a name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects knobs the defaults would never produce", func(t *testing.T) {
		t.Parallel()

		for name, mutate := range map[string]func(*Config){
			"sub-second retention":     func(cfg *Config) { cfg.Retention = time.Millisecond },
			"empty claim batch":        func(cfg *Config) { cfg.MaxClaimBatch = -1 },
			"empty reap batch":         func(cfg *Config) { cfg.ReapBatchSize = -1 },
			"sub-millisecond wake":     func(cfg *Config) { cfg.MinWakeInterval = time.Microsecond },
			"no write attempts at all": func(cfg *Config) { cfg.WriteAttempts = 0 },
		} {
			cfg := validConfig()
			cfg.EnsureDefaults()
			mutate(cfg)

			test.Error(t, cfg.ValidateWithContext(t.Context()), test.Sprintf("case %q", name))
		}
	})
}

func TestConfig_AttemptCeiling(T *testing.T) {
	T.Parallel()

	T.Run("passes an ordinary ceiling through", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{MaxAttempts: 5}

		test.EqOp(t, 5, cfg.attemptCeiling())
	})

	// A timer set defaults to a real ceiling, unlike a work queue, so unlimited
	// has to survive EnsureDefaults — which it can only do by being expressible
	// as something other than the zero value.
	T.Run("a negative ceiling is unlimited and survives defaulting", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Name: "trials", MaxAttempts: -1}
		cfg.EnsureDefaults()

		must.EqOp(t, -1, cfg.MaxAttempts)
		test.EqOp(t, 0, cfg.attemptCeiling())
	})

	// Wrapping to a negative would read as "unlimited" on the way into the
	// predicate, silently turning the stall guard off — so it saturates.
	T.Run("saturates rather than wrapping", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{MaxAttempts: math.MaxInt32 + 1}

		must.EqOp(t, math.MaxInt32, cfg.attemptCeiling())
		test.True(t, cfg.attemptCeiling() > 0)
	})
}

func TestWorkerConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills the unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultWorkerPoll, cfg.Poll)
		test.EqOp(t, DefaultWorkerLease, cfg.Lease)
		test.EqOp(t, DefaultWorkerRetryDelay, cfg.RetryDelay)
		test.EqOp(t, DefaultWorkerBatch, cfg.Batch)
		test.EqOp(t, DefaultWorkerConcurrency, cfg.Concurrency)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects knobs the defaults would never produce", func(t *testing.T) {
		t.Parallel()

		for name, mutate := range map[string]func(*WorkerConfig){
			"no poll":        func(cfg *WorkerConfig) { cfg.Poll = 0 },
			"no lease":       func(cfg *WorkerConfig) { cfg.Lease = 0 },
			"no retry delay": func(cfg *WorkerConfig) { cfg.RetryDelay = 0 },
			"empty batch":    func(cfg *WorkerConfig) { cfg.Batch = 0 },
			"no concurrency": func(cfg *WorkerConfig) { cfg.Concurrency = -1 },
		} {
			cfg := &WorkerConfig{}
			cfg.EnsureDefaults()
			mutate(cfg)

			test.Error(t, cfg.ValidateWithContext(t.Context()), test.Sprintf("case %q", name))
		}
	})
}

func TestTableFor(T *testing.T) {
	T.Parallel()

	T.Run("an empty namespace renders the component's own name", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "scheduled_timers", tableFor(""))
	})

	T.Run("a namespace is separated by the renderer", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "app_scheduled_timers", tableFor("app"))
	})
}

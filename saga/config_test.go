package saga

import (
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestWorkerConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills every unset knob", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultPollInterval, cfg.PollInterval)
		test.EqOp(t, DefaultStatsInterval, cfg.StatsInterval)
		test.EqOp(t, DefaultLeaseDuration, cfg.LeaseDuration)
		test.EqOp(t, DefaultLockTTL, cfg.LockTTL)
		test.EqOp(t, DefaultAdvanceTimeout, cfg.AdvanceTimeout)
		test.EqOp(t, DefaultStepTimeout, cfg.StepTimeout)
		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultConcurrency, cfg.Concurrency)
		test.EqOp(t, DefaultLockKeyPrefix, cfg.LockKeyPrefix)
		test.EqOp(t, DefaultIdempotencyKeyPrefix, cfg.IdempotencyKeyPrefix)
		test.EqOp(t, DefaultCompensationAttempts, cfg.CompensationBackoff.MaxAttempts)
		test.True(t, cfg.Backoff.MaxAttempts > 0)

		// The defaults must satisfy their own validation, or nobody can start a
		// worker without reading the source.
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			PollInterval:         2 * time.Second,
			StatsInterval:        90 * time.Second,
			LeaseDuration:        20 * time.Minute,
			LockTTL:              21 * time.Minute,
			AdvanceTimeout:       4 * time.Minute,
			StepTimeout:          time.Minute,
			BatchSize:            7,
			Concurrency:          3,
			LockKeyPrefix:        "custom:",
			IdempotencyKeyPrefix: "idem:",
		}
		cfg.CompensationBackoff.MaxAttempts = 4
		cfg.EnsureDefaults()

		test.EqOp(t, 2*time.Second, cfg.PollInterval)
		test.EqOp(t, 90*time.Second, cfg.StatsInterval)
		test.EqOp(t, 7, cfg.BatchSize)
		test.EqOp(t, "custom:", cfg.LockKeyPrefix)
		test.EqOp(t, uint(4), cfg.CompensationBackoff.MaxAttempts)

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestWorkerConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("rejects a lease that cannot outlast a pass", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.LeaseDuration = cfg.AdvanceTimeout

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a lock TTL that cannot outlast a pass", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.LockTTL = cfg.AdvanceTimeout

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a step that cannot fit inside a pass", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.StepTimeout = cfg.AdvanceTimeout + time.Second

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects a zeroed config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&WorkerConfig{}).ValidateWithContext(t.Context()))
	})

	T.Run("rejects an absent stats interval", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.StatsInterval = 0

		// A worker whose stats ticker never fires is a worker whose stuck level
		// is only ever recorded by whoever remembers to call Worker.Stats — so
		// it is refused rather than defaulted to off.
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an empty key prefix", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.IdempotencyKeyPrefix = ""

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestWorkerConfig_budgetFor(T *testing.T) {
	T.Parallel()

	T.Run("compensation gets its own budget", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, cfg.Backoff.MaxAttempts, cfg.budgetFor(phaseDo).MaxAttempts)
		test.EqOp(t, cfg.CompensationBackoff.MaxAttempts, cfg.budgetFor(phaseUndo).MaxAttempts)

		// Giving up on a Do costs a compensation; giving up on an Undo costs an
		// operator's evening, so the second budget is the larger one.
		test.True(t, cfg.CompensationBackoff.MaxAttempts > cfg.Backoff.MaxAttempts)
	})
}

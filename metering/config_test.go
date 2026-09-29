package metering

import (
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRecorderConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &RecorderConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("keeps what was set", func(t *testing.T) {
		t.Parallel()

		cfg := &RecorderConfig{BatchSize: 7, AllowUnknownMeters: true}
		cfg.EnsureDefaults()

		test.EqOp(t, 7, cfg.BatchSize)
		test.True(t, cfg.AllowUnknownMeters)
	})

	T.Run("clamps a non-positive batch size", func(t *testing.T) {
		t.Parallel()

		cfg := &RecorderConfig{BatchSize: -1}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
	})

	T.Run("rejects an undefaulted config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&RecorderConfig{}).ValidateWithContext(t.Context()))
	})
}

func TestEnforcerConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &EnforcerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultStaleness, cfg.Staleness)
		test.EqOp(t, DefaultCachePrefix, cfg.CachePrefix)
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("keeps what was set", func(t *testing.T) {
		t.Parallel()

		cfg := &EnforcerConfig{Staleness: time.Second, CachePrefix: "usage:", FailOpen: true}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Second, cfg.Staleness)
		test.EqOp(t, "usage:", cfg.CachePrefix)
		test.True(t, cfg.FailOpen)
	})

	T.Run("rejects an undefaulted config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&EnforcerConfig{}).ValidateWithContext(t.Context()))
	})
}

func TestFlusherConfig(T *testing.T) {
	T.Parallel()

	T.Run("fills defaults", func(t *testing.T) {
		t.Parallel()

		cfg := &FlusherConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultFlushLeaseDuration, cfg.LeaseDuration)
		test.EqOp(t, DefaultFlushTimeout, cfg.FlushTimeout)
		test.EqOp(t, DefaultEventRetention, cfg.EventRetention)
		test.EqOp(t, DefaultFlushBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultFlushConcurrency, cfg.Concurrency)
		test.EqOp(t, DefaultMaxFlushAttempts, cfg.MaxAttempts)
		test.EqOp(t, DefaultReapBatchSize, cfg.ReapBatchSize)
		test.Greater(t, uint(0), cfg.Backoff.MaxAttempts)

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("keeps what was set", func(t *testing.T) {
		t.Parallel()

		cfg := &FlusherConfig{
			LeaseDuration:  2 * time.Minute,
			FlushTimeout:   time.Second,
			EventRetention: time.Hour,
			BatchSize:      7,
			Concurrency:    2,
			MaxAttempts:    3,
			ReapBatchSize:  11,
			DisableReap:    true,
		}
		cfg.EnsureDefaults()

		test.EqOp(t, 7, cfg.BatchSize)
		test.True(t, cfg.DisableReap)

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("refuses a lease that cannot cover a post", func(t *testing.T) {
		t.Parallel()

		// The one cross-field rule in this package. Two flushers posting the same
		// total concurrently produces two sequence numbers for one delta, which is
		// the duplicate charge no idempotency key can undo.
		cfg := &FlusherConfig{FlushTimeout: time.Minute, LeaseDuration: time.Minute}
		cfg.EnsureDefaults()

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("accepts a lease that comfortably exceeds a post", func(t *testing.T) {
		t.Parallel()

		cfg := &FlusherConfig{FlushTimeout: time.Minute, LeaseDuration: time.Minute + time.Nanosecond}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an undefaulted config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&FlusherConfig{}).ValidateWithContext(t.Context()))
	})
}

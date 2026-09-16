package webhooks

import (
	"math"
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

		test.EqOp(t, DefaultBatchSize, cfg.BatchSize)
		test.EqOp(t, DefaultConcurrency, cfg.Concurrency)
		test.EqOp(t, DefaultPollInterval, cfg.PollInterval)
		test.EqOp(t, DefaultLeaseDuration, cfg.LeaseDuration)
		test.EqOp(t, DefaultRequestTimeout, cfg.RequestTimeout)
		test.EqOp(t, DefaultCircuitOpenRetryDelay, cfg.CircuitOpenRetryDelay)
		test.EqOp(t, DefaultRetention, cfg.Retention)
		test.EqOp(t, DefaultReapInterval, cfg.ReapInterval)
		test.EqOp(t, DefaultReapBatchSize, cfg.ReapBatchSize)
		test.EqOp(t, DefaultUserAgent, cfg.UserAgent)
		test.True(t, cfg.Backoff.MaxAttempts > 0)
	})

	T.Run("leaves explicit values alone", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			BatchSize:      7,
			Concurrency:    3,
			RequestTimeout: 2 * time.Second,
			UserAgent:      "acme-hooks/2",
		}
		cfg.EnsureDefaults()

		test.EqOp(t, 7, cfg.BatchSize)
		test.EqOp(t, 3, cfg.Concurrency)
		test.EqOp(t, 2*time.Second, cfg.RequestTimeout)
		test.EqOp(t, "acme-hooks/2", cfg.UserAgent)
	})

	// The defaults must satisfy their own validation, or a caller who configures
	// nothing cannot construct a Worker.
	T.Run("the defaults validate", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// Spelled out rather than left to the rule above, because the arithmetic is
	// what the defaults got wrong: a lease of a minute cleared one ten-second
	// request and not the seven waves a batch of a hundred takes sixteen at a
	// time. Whoever next moves one of the four constants is moving this.
	T.Run("the default lease clears the batch the other defaults describe", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, 70*time.Second, cfg.batchBound())
		test.True(t, DefaultLeaseDuration > cfg.batchBound())
	})
}

func TestWorkerConfig_batchBound(T *testing.T) {
	T.Parallel()

	T.Run("rounds a partial wave up", func(t *testing.T) {
		t.Parallel()

		// Seventeen sixteen at a time is two waves, not one and a sixteenth.
		cfg := &WorkerConfig{BatchSize: 17, Concurrency: 16, RequestTimeout: 10 * time.Second}

		test.EqOp(t, 20*time.Second, cfg.batchBound())
	})

	T.Run("a batch that fits in one wave is one request", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{BatchSize: 4, Concurrency: 8, RequestTimeout: 10 * time.Second}

		test.EqOp(t, 10*time.Second, cfg.batchBound())
	})

	// Zero says "somebody else's rule reports this", so the lease is not named
	// for a batch size or a concurrency that is out of range.
	T.Run("reports zero for a knob out of range", func(t *testing.T) {
		t.Parallel()

		for _, cfg := range []*WorkerConfig{
			{BatchSize: 0, Concurrency: 16, RequestTimeout: time.Second},
			{BatchSize: 100, Concurrency: 0, RequestTimeout: time.Second},
			{BatchSize: 100, Concurrency: 16, RequestTimeout: 0},
		} {
			test.EqOp(t, 0, cfg.batchBound())
		}
	})

	// A batch nothing could cover reports a bound nothing can clear, rather than
	// wrapping to a negative one every lease clears.
	T.Run("saturates instead of overflowing", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{BatchSize: math.MaxInt32, Concurrency: 1, RequestTimeout: time.Hour}

		test.EqOp(t, time.Duration(math.MaxInt64), cfg.batchBound())
	})
}

func TestWorkerConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	// The lease has to outlast the request it covers. A shorter one expires
	// mid-flight, a second worker reclaims the dispatch, and the subscriber gets
	// the same payload twice from two workers at once.
	T.Run("rejects a lease that does not outlast a request", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.RequestTimeout = cfg.LeaseDuration

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), ErrLeaseTooShort.Error())
	})

	// The shape this package shipped: a lease that outlasts any one request and
	// expires partway through the batch holding it.
	T.Run("rejects a lease that clears a request but not the batch", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			BatchSize:      100,
			Concurrency:    16,
			RequestTimeout: 10 * time.Second,
			LeaseDuration:  60 * time.Second,
		}
		cfg.EnsureDefaults()

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), ErrLeaseTooShort.Error())
	})

	// The bound itself is not enough: a lease that expires exactly as the last
	// wave's timeout does is a lease the reclaim races.
	T.Run("rejects a lease equal to the batch bound", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			BatchSize:      100,
			Concurrency:    16,
			RequestTimeout: 10 * time.Second,
			LeaseDuration:  70 * time.Second,
		}
		cfg.EnsureDefaults()

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), ErrLeaseTooShort.Error())
	})

	T.Run("accepts a lease longer than the batch", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			BatchSize:      100,
			Concurrency:    16,
			RequestTimeout: 10 * time.Second,
			LeaseDuration:  71 * time.Second,
		}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// A batch of one is the case where the batch bound and the request timeout
	// are the same number, and the old rule and the new one agree.
	T.Run("accepts a lease longer than a request when the batch is one wave", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{
			BatchSize:      8,
			Concurrency:    16,
			RequestTimeout: 10 * time.Second,
			LeaseDuration:  11 * time.Second,
		}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// The lease is not named for somebody else's knob being out of range.
	T.Run("leaves an out-of-range concurrency to its own rule", func(t *testing.T) {
		t.Parallel()

		cfg := &WorkerConfig{}
		cfg.EnsureDefaults()
		cfg.Concurrency = 0

		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "concurrency")
		test.StrNotContains(t, err.Error(), ErrLeaseTooShort.Error())
	})

	T.Run("rejects a zero config", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&WorkerConfig{}).ValidateWithContext(t.Context()))
	})
}

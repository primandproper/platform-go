package timerscfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/timers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// clientFor is a database.Client that reports one dialect and nothing else.
// Neither constructor touches the pools; the set reads the dialect off it.
func clientFor(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return d },
	}
}

func validConfig() *Config {
	return &Config{Timers: timers.Config{Name: "trials"}}
}

func noopHandler(context.Context, timers.Due[string]) error { return nil }

func TestConfig(T *testing.T) {
	T.Parallel()

	T.Run("defaults both halves", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, timers.DefaultRetention, cfg.Timers.Retention)
		test.EqOp(t, timers.DefaultWorkerBatch, cfg.Worker.Batch)

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	// ozzo dereferences a struct-value field before checking
	// ValidatableWithContext, so without the By closures a broken nested config
	// would validate clean.
	T.Run("reaches into both halves to validate them", func(t *testing.T) {
		t.Parallel()

		unnamed := validConfig()
		unnamed.EnsureDefaults()
		unnamed.Timers.Name = ""
		test.Error(t, unnamed.ValidateWithContext(t.Context()))

		badWorker := validConfig()
		badWorker.EnsureDefaults()
		badWorker.Worker.Batch = -1
		test.Error(t, badWorker.ValidateWithContext(t.Context()))
	})
}

func TestNewTimers(T *testing.T) {
	T.Parallel()

	T.Run("builds a set from configuration", func(t *testing.T) {
		t.Parallel()

		set, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, set)

		test.EqOp(t, "trials", set.Name())
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), nil, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, timers.ErrNilConfig)
	})

	// Each of the four config-derived options is forwarded only when it was
	// actually set, so an absent one leaves the set on its own default rather
	// than handing the leaf package a nil to resolve.
	T.Run("forwards the options it was given", func(t *testing.T) {
		t.Parallel()

		var c clock.Clock = clock.NewClock()

		set, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithClock(c),
			WithLogger(logging.EnsureLogger(nil)),
			WithTracerProvider(tracing.EnsureTracerProvider(nil)),
			WithMetricsProvider(metrics.EnsureMetricsProvider(nil)),
		)

		must.NoError(t, err)
		test.EqOp(t, c, set.Clock())
	})

	// Explicit timers options run after the config-derived ones, which is what
	// makes overriding a configured value possible.
	T.Run("lets an explicit option override a configured one", func(t *testing.T) {
		t.Parallel()

		var override clock.Clock = clock.NewClock()

		set, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithClock(clock.NewClock()),
			WithTimerOptions(timers.WithClock(override)),
		)

		must.NoError(t, err)
		test.EqOp(t, override, set.Clock())
	})

	// Defaulting and validation belong to timers.New; this pins that they still
	// happen for a config that arrives through here.
	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), &Config{}, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, timers.ErrEmptySetName)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, timers.ErrNilDatabaseClient)
	})

	T.Run("surfaces the dialect the set refuses", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.SQLite))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("derives options from every observability argument", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithPillars(nil),
			WithClock(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
	})

	// A codec is a Go value the environment cannot name, so the passthrough is
	// the only way one reaches a set built from configuration.
	T.Run("passes timer options through", func(t *testing.T) {
		t.Parallel()

		_, err := NewTimers[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithTimerOptions(timers.WithKeyCodec(timers.DefaultKeyCodec[int]())))

		// The mismatched codec is the proof it arrived: a passthrough that
		// dropped it would build cleanly.
		test.ErrorIs(t, err, timers.ErrKeyCodecTypeMismatch)
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("builds a worker over the set it returns", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Worker.Batch = 3

		worker, set, err := NewWorker(t.Context(), cfg, clientFor(dialect.Postgres), noopHandler)
		must.NoError(t, err)
		must.NotNil(t, worker)
		must.NotNil(t, set)

		test.EqOp(t, "trials", set.Name())
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, _, err := NewWorker(t.Context(), nil, clientFor(dialect.Postgres), noopHandler)
		test.ErrorIs(t, err, timers.ErrNilConfig)
	})

	T.Run("surfaces a failure from either half", func(t *testing.T) {
		t.Parallel()

		_, _, err := NewWorker(t.Context(), &Config{}, clientFor(dialect.Postgres), noopHandler)
		test.ErrorIs(t, err, timers.ErrEmptySetName)

		_, _, err = NewWorker[string](t.Context(), validConfig(), clientFor(dialect.Postgres), nil)
		test.ErrorIs(t, err, timers.ErrNilHandler)
	})

	T.Run("honors an explicitly configured worker", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Worker.Poll = 5 * time.Second

		_, _, err := NewWorker(t.Context(), cfg, clientFor(dialect.Postgres), noopHandler)
		must.NoError(t, err)

		test.EqOp(t, 5*time.Second, cfg.Worker.Poll)
	})
}

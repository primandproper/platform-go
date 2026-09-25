package workqueuecfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// clientFor is a database.Client that reports one dialect and nothing else.
// NewQueue reads the dialect off it and never touches the pools.
func clientFor(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return d },
	}
}

func validConfig() *workqueue.Config {
	return &workqueue.Config{Name: "jobs"}
}

func TestNewQueue(T *testing.T) {
	T.Parallel()

	T.Run("builds a queue from configuration", func(t *testing.T) {
		t.Parallel()

		q, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, q)
		t.Cleanup(func() { _ = q.Close(t.Context()) })

		test.EqOp(t, "jobs", q.Name())
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueue[string](t.Context(), nil, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, workqueue.ErrNilConfig)
	})

	// Defaulting and validation belong to workqueue.New; this pins that they
	// still happen for a config that arrives through here.
	T.Run("rejects an invalid config", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueue[string](t.Context(), &workqueue.Config{}, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, workqueue.ErrEmptyQueueName)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueue[string](t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, workqueue.ErrNilDatabaseClient)
	})

	T.Run("surfaces the dialect the queue refuses", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.Dialect("oracle")))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("derives options from every observability argument", func(t *testing.T) {
		t.Parallel()

		q, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithPillars(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(t.Context()) })
	})

	// A codec is a Go value the environment cannot name, so the passthrough is
	// the only way one reaches a queue built from configuration.
	T.Run("passes queue options through", func(t *testing.T) {
		t.Parallel()

		_, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithQueueOptions(workqueue.WithKeyCodec(workqueue.DefaultKeyCodec[int]())))

		// The mismatched codec is the proof it arrived: a passthrough that
		// dropped it would build cleanly.
		test.ErrorIs(t, err, workqueue.ErrKeyCodecTypeMismatch)
	})
}

// newTestQueue builds the queue NewRunner drains, closed when the test ends.
func newTestQueue(t *testing.T) *workqueue.Queue[string] {
	t.Helper()

	q, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
	must.NoError(t, err)

	t.Cleanup(func() { _ = q.Close(t.Context()) })

	return q
}

func noopHandler(context.Context, workqueue.Item[string]) error { return nil }

func TestNewRunner(T *testing.T) {
	T.Parallel()

	T.Run("builds a runner over a queue", func(t *testing.T) {
		t.Parallel()

		r, err := NewRunner(t.Context(), &workqueue.RunnerConfig{}, newTestQueue(t), noopHandler)
		must.NoError(t, err)
		test.NotNil(t, r)
	})

	// Defaulting and validation belong to workqueue.NewRunner; this pins that
	// they still happen for a config that arrives through here.
	T.Run("rejects the inputs the runner refuses", func(t *testing.T) {
		t.Parallel()

		_, err := NewRunner(t.Context(), nil, newTestQueue(t), noopHandler)
		test.ErrorIs(t, err, workqueue.ErrNilConfig)

		_, err = NewRunner[string](t.Context(), &workqueue.RunnerConfig{}, nil, noopHandler)
		test.ErrorIs(t, err, workqueue.ErrNilQueue)

		_, err = NewRunner[string](t.Context(), &workqueue.RunnerConfig{}, newTestQueue(t), nil)
		test.ErrorIs(t, err, workqueue.ErrNilHandler)
	})

	T.Run("derives options from every observability argument", func(t *testing.T) {
		t.Parallel()

		_, err := NewRunner(t.Context(), &workqueue.RunnerConfig{}, newTestQueue(t), noopHandler,
			WithPillars(nil),
			WithLogger(nil),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
	})

	// The runner's passthrough is separate from the queue's, so an option meant
	// for the loop cannot land on the queue it drains.
	T.Run("passes runner options through", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithRunnerOptions(workqueue.WithRunnerLogger(nil)),
			WithQueueOptions(workqueue.WithLogger(nil)),
		})

		test.SliceLen(t, 1, o.runner)
		test.SliceLen(t, 1, o.queue)
	})
}

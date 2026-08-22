package workqueuecfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	databasemock "github.com/primandproper/platform-go/v13/database/mock"
	"github.com/primandproper/platform-go/v13/workqueue"

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

		_, err := NewQueue[string](t.Context(), validConfig(), clientFor(dialect.SQLite))
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

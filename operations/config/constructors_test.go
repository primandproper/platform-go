package operationscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// clientFor builds a database.Client that reports d and nothing else. Neither
// the store nor the queue opens a connection at construction — both read the
// dialect and refuse anything but Postgres — so this is the whole dependency.
func clientFor(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return d },
	}
}

func postgresClient() database.Client { return clientFor(dialect.Postgres) }

func allPillars() *observability.Pillars {
	return &observability.Pillars{
		Logger:          loggingnoop.NewLogger(),
		TracerProvider:  tracingnoop.NewTracerProvider(),
		MetricsProvider: metricsnoop.NewMetricsProvider(),
	}
}

func TestNewStore(T *testing.T) {
	T.Parallel()

	T.Run("builds a store over a Postgres client", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(&Config{}, postgresClient())
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(nil, postgresClient())
		test.ErrorIs(t, err, operations.ErrNilConfig)
		test.Nil(t, store)
	})

	T.Run("a nil client is refused", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(&Config{}, nil)
		test.ErrorIs(t, err, operations.ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite} {
		T.Run("refuses "+string(d)+" rather than building a store that cannot run its SQL", func(t *testing.T) {
			t.Parallel()

			// This package's SQL is written against Postgres rather than reduced
			// to a portable subset, so the refusal belongs at construction.
			store, err := NewStore(&Config{}, clientFor(d))
			test.ErrorIs(t, err, dialect.ErrUnsupported)
			test.Nil(t, store)
		})
	}

	T.Run("the pillars reach the store", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(&Config{}, postgresClient(), WithPillars(allPillars()))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a bad table prefix from configuration is reported", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.Operations.TablePrefix = "no-hyphens-allowed"

		store, err := NewStore(cfg, postgresClient())
		must.Error(t, err)
		test.Nil(t, store)
	})

	T.Run("store options are applied after the ones configuration derived", func(t *testing.T) {
		t.Parallel()

		// The ordering is what lets a caller override anything the environment
		// set, so passing a valid prefix through the option must win over the
		// invalid one in the config.
		cfg := &Config{}
		cfg.Operations.TablePrefix = "bad-prefix"

		store, err := NewStore(cfg, postgresClient(),
			WithStoreOptions(operations.WithStoreTablePrefix("good_prefix")))
		must.NoError(t, err)
		test.NotNil(t, store)
	})
}

func TestNewQueue(T *testing.T) {
	T.Parallel()

	T.Run("builds a queue and names it from the operations config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.Operations.QueueName = "exports"

		queue, err := NewQueue(t.Context(), cfg, postgresClient())
		must.NoError(t, err)
		must.NotNil(t, queue)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })

		test.EqOp(t, "exports", cfg.Queue.Name)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		queue, err := NewQueue(t.Context(), nil, postgresClient())
		test.ErrorIs(t, err, operations.ErrNilConfig)
		test.Nil(t, queue)
	})

	T.Run("a nil client is refused", func(t *testing.T) {
		t.Parallel()

		queue, err := NewQueue(t.Context(), &Config{}, nil)
		test.ErrorIs(t, err, workqueue.ErrNilDatabaseClient)
		test.Nil(t, queue)
	})

	T.Run("a non-Postgres client is refused", func(t *testing.T) {
		t.Parallel()

		queue, err := NewQueue(t.Context(), &Config{}, clientFor(dialect.MySQL))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.Nil(t, queue)
	})

	T.Run("the pillars and a wakeup channel reach the queue", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{})

		queue, err := NewQueue(t.Context(), &Config{}, postgresClient(),
			WithPillars(allPillars()),
			WithQueueWakeup(wakeup),
		)
		must.NoError(t, err)
		must.NotNil(t, queue)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})

	T.Run("queue options carry a key codec the environment cannot name", func(t *testing.T) {
		t.Parallel()

		// A codec is a Go value, so WithQueueOptions is the only way it reaches
		// a queue built from configuration. A codec for the wrong key type is
		// caught here rather than after keys have been written under a
		// rendering nothing will decode.
		queue, err := NewQueue(t.Context(), &Config{}, postgresClient(),
			WithQueueOptions(workqueue.WithKeyCodec(workqueue.DefaultKeyCodec[int]())))
		test.ErrorIs(t, err, workqueue.ErrKeyCodecTypeMismatch)
		test.Nil(t, queue)
	})
}

func TestNewService(T *testing.T) {
	T.Parallel()

	T.Run("builds the store, the queue, and the service over both", func(t *testing.T) {
		t.Parallel()

		svc, queue, err := NewService(t.Context(), &Config{}, postgresClient(), operations.NewRegistry())
		must.NoError(t, err)
		must.NotNil(t, svc)
		must.NotNil(t, queue)

		// The queue comes back because it owns a goroutine the caller has to
		// close, and because a worker in the same process needs the same value.
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})

	T.Run("a nil config is refused before anything is built", func(t *testing.T) {
		t.Parallel()

		svc, queue, err := NewService(t.Context(), nil, postgresClient(), operations.NewRegistry())
		test.ErrorIs(t, err, operations.ErrNilConfig)
		test.Nil(t, svc)
		test.Nil(t, queue)
	})

	T.Run("a store that cannot be built stops the service", func(t *testing.T) {
		t.Parallel()

		svc, queue, err := NewService(t.Context(), &Config{}, clientFor(dialect.SQLite), operations.NewRegistry())
		test.ErrorIs(t, err, dialect.ErrUnsupported)
		test.Nil(t, svc)
		test.Nil(t, queue)
	})

	T.Run("a queue that cannot be built stops the service", func(t *testing.T) {
		t.Parallel()

		svc, queue, err := NewService(t.Context(), &Config{}, postgresClient(), operations.NewRegistry(),
			WithQueueOptions(workqueue.WithKeyCodec(workqueue.DefaultKeyCodec[int]())))
		test.ErrorIs(t, err, workqueue.ErrKeyCodecTypeMismatch)
		test.Nil(t, svc)
		test.Nil(t, queue)
	})

	T.Run("a service that cannot be built gives the queue's goroutine back", func(t *testing.T) {
		t.Parallel()

		// The failure path that matters: the queue is already running by the
		// time the service is refused, so a process reporting a configuration
		// error at boot must not also leak a batcher.
		svc, queue, err := NewService(t.Context(), &Config{}, postgresClient(), nil)
		test.ErrorIs(t, err, operations.ErrNilRegistry)
		test.Nil(t, svc)
		test.Nil(t, queue)
	})

	T.Run("the pillars reach every half", func(t *testing.T) {
		t.Parallel()

		svc, queue, err := NewService(t.Context(), &Config{}, postgresClient(), operations.NewRegistry(),
			WithPillars(allPillars()),
			WithServiceOptions(),
		)
		must.NoError(t, err)
		must.NotNil(t, svc)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	// queueFor builds the queue the worker and service share, closed by the test.
	queueFor := func(t *testing.T, cfg *Config) *workqueue.Queue[string] {
		t.Helper()

		queue, err := NewQueue(t.Context(), cfg, postgresClient())
		must.NoError(t, err)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })

		return queue
	}

	T.Run("builds a worker over an existing store and queue", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		worker, err := NewWorker(t.Context(), cfg, stubStore{}, queueFor(t, cfg), operations.NewRegistry())
		must.NoError(t, err)
		test.NotNil(t, worker)
	})

	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		worker, err := NewWorker(t.Context(), nil, stubStore{}, queueFor(t, &Config{}), operations.NewRegistry())
		test.ErrorIs(t, err, operations.ErrNilConfig)
		test.Nil(t, worker)
	})

	T.Run("a nil store is refused", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		worker, err := NewWorker(t.Context(), cfg, nil, queueFor(t, cfg), operations.NewRegistry())
		test.ErrorIs(t, err, operations.ErrNilStore)
		test.Nil(t, worker)
	})

	T.Run("a nil queue is refused", func(t *testing.T) {
		t.Parallel()

		worker, err := NewWorker(t.Context(), &Config{}, stubStore{}, nil, operations.NewRegistry())
		test.ErrorIs(t, err, operations.ErrNilQueue)
		test.Nil(t, worker)
	})

	T.Run("a nil registry is refused", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		worker, err := NewWorker(t.Context(), cfg, stubStore{}, queueFor(t, cfg), nil)
		test.ErrorIs(t, err, operations.ErrNilRegistry)
		test.Nil(t, worker)
	})

	T.Run("the pillars reach the worker", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		worker, err := NewWorker(t.Context(), cfg, stubStore{}, queueFor(t, cfg), operations.NewRegistry(),
			WithPillars(allPillars()),
			WithWorkerOptions(),
		)
		must.NoError(t, err)
		test.NotNil(t, worker)
	})
}

func TestNewWatcher_Options(T *testing.T) {
	T.Parallel()

	// The construction and nil-store cases are in TestNewWatcher; what is left
	// is the option translation.
	T.Run("a nil config is refused", func(t *testing.T) {
		t.Parallel()

		watcher, err := NewWatcher(t.Context(), nil, stubStore{})
		test.ErrorIs(t, err, operations.ErrNilConfig)
		test.Nil(t, watcher)
	})

	T.Run("the pillars and a wakeup channel reach the watcher", func(t *testing.T) {
		t.Parallel()

		// A separate channel from the queue's: one fires when work is enqueued,
		// the other when a row changes, and sharing one would wake every watcher
		// on every enqueue.
		wakeup := make(chan struct{})

		watcher, err := NewWatcher(t.Context(), &Config{}, stubStore{},
			WithPillars(allPillars()),
			WithWatcherWakeup(wakeup),
			WithWatcherOptions(),
		)
		must.NoError(t, err)
		must.NotNil(t, watcher)
		t.Cleanup(func() { _ = watcher.Close() })
	})
}

func TestWithTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("attaches the tracer provider", func(t *testing.T) {
		t.Parallel()

		tp := tracingnoop.NewTracerProvider()

		o := newOptions([]Option{WithTracerProvider(tp)})

		test.Eq(t, tp, o.tracerProvider)
	})
}

// container wires the registrations a test needs, minus the ones it is checking
// the absence of.
func container(t *testing.T, client database.Client) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, client)
	do.ProvideValue(i, &Config{})

	return i
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("resolves a store", func(t *testing.T) {
		t.Parallel()

		i := container(t, postgresClient())
		RegisterStore(i)

		store, err := do.Invoke[operations.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("a container registering no observability still wires up", func(t *testing.T) {
		t.Parallel()

		// Absent pillars are fine; only a registered one that fails to build is
		// an error, which is what keeps a misconfigured exporter from
		// degrading into a noop that looks configured.
		i := container(t, postgresClient())
		RegisterStore(i)

		_, err := do.Invoke[operations.Store](i)
		test.NoError(t, err)
	})

	T.Run("reports a store it cannot build", func(t *testing.T) {
		t.Parallel()

		i := container(t, clientFor(dialect.MySQL))
		RegisterStore(i)

		_, err := do.Invoke[operations.Store](i)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestRegisterQueue(T *testing.T) {
	T.Parallel()

	T.Run("resolves a queue", func(t *testing.T) {
		t.Parallel()

		i := container(t, postgresClient())
		RegisterQueue(i)

		queue, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		must.NotNil(t, queue)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})

	T.Run("reports a queue it cannot build", func(t *testing.T) {
		t.Parallel()

		i := container(t, clientFor(dialect.SQLite))
		RegisterQueue(i)

		_, err := do.Invoke[*workqueue.Queue[string]](i)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("resolves a service over the registered store and queue", func(t *testing.T) {
		t.Parallel()

		i := container(t, postgresClient())
		do.ProvideValue(i, operations.NewRegistry())

		RegisterStore(i)
		RegisterQueue(i)
		RegisterService(i)

		svc, err := do.Invoke[operations.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)

		queue, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})

	T.Run("the service and the worker resolve the same queue", func(t *testing.T) {
		t.Parallel()

		// The queue is registered separately precisely so both halves share one
		// value: two queues over one table would mean two enqueue batchers,
		// which is the part that only pays off when there is exactly one.
		i := container(t, postgresClient())
		do.ProvideValue(i, operations.NewRegistry())

		RegisterQueue(i)

		first, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = first.Close(t.Context()) })

		second, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)

		test.Eq(t, first, second)
	})
}

func TestRegisterWorker(T *testing.T) {
	T.Parallel()

	T.Run("resolves a worker", func(t *testing.T) {
		t.Parallel()

		i := container(t, postgresClient())
		do.ProvideValue(i, operations.NewRegistry())

		RegisterStore(i)
		RegisterQueue(i)
		RegisterWorker(i)

		worker, err := do.Invoke[*operations.Worker](i)
		must.NoError(t, err)
		test.NotNil(t, worker)

		queue, err := do.Invoke[*workqueue.Queue[string]](i)
		must.NoError(t, err)
		t.Cleanup(func() { _ = queue.Close(t.Context()) })
	})
}

func TestRegisterWatcher(T *testing.T) {
	T.Parallel()

	T.Run("resolves a watcher", func(t *testing.T) {
		t.Parallel()

		i := container(t, postgresClient())

		RegisterStore(i)
		RegisterWatcher(i)

		watcher, err := do.Invoke[*operations.Watcher](i)
		must.NoError(t, err)
		must.NotNil(t, watcher)
		t.Cleanup(func() { _ = watcher.Close() })
	})
}

// A registered observability provider that fails to build has to reach the
// caller. Treating it as absent would hand the component a noop and leave a
// misconfigured exporter looking configured — see observability.InvokePillars.
func TestRegister_failingObservabilityIsAnError(T *testing.T) {
	T.Parallel()

	// Asserted by identity, not merely that some error came back: a missing
	// dependency would also fail, and would not exercise this branch.
	errBuild := platformerrors.New("building the metrics provider")

	for _, tc := range []struct {
		register func(do.Injector)
		invoke   func(do.Injector) error
		name     string
	}{
		{
			name:     "RegisterStore",
			register: RegisterStore,
			invoke:   func(i do.Injector) error { _, err := do.Invoke[operations.Store](i); return err },
		},
		{
			name:     "RegisterQueue",
			register: RegisterQueue,
			invoke: func(i do.Injector) error {
				_, err := do.Invoke[*workqueue.Queue[string]](i)

				return err
			},
		},
		{
			name:     "RegisterService",
			register: RegisterService,
			invoke:   func(i do.Injector) error { _, err := do.Invoke[operations.Service](i); return err },
		},
		{
			name:     "RegisterWorker",
			register: RegisterWorker,
			invoke:   func(i do.Injector) error { _, err := do.Invoke[*operations.Worker](i); return err },
		},
		{
			name:     "RegisterWatcher",
			register: RegisterWatcher,
			invoke:   func(i do.Injector) error { _, err := do.Invoke[*operations.Watcher](i); return err },
		},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			i := container(t, postgresClient())
			do.ProvideValue(i, operations.NewRegistry())
			do.Provide(i, func(do.Injector) (metrics.Provider, error) {
				return nil, errBuild
			})

			tc.register(i)

			test.ErrorIs(t, tc.invoke(i), errBuild)
		})
	}
}

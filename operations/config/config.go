package operationscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/workqueue"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures the operations tier.
type Config struct {
	// Operations configures the store and the service over it: the table, the
	// queue name, and the retention and recovery policy.
	Operations operations.Config `env:",init" json:"operations,omitzero" yaml:"operations,omitempty"`

	// Queue configures the work queue operations are dispatched through.
	//
	// Its Name is filled from Operations.QueueName by EnsureDefaults rather than
	// being configured twice. Two names for one queue is a misconfiguration
	// whose only symptom is a table of pending operations that nothing ever
	// claims.
	Queue workqueue.Config `env:",init" envPrefix:"QUEUE_" json:"queue,omitzero" yaml:"queue,omitempty"`

	// Worker configures the run loop. It is inert for a process that only starts
	// and reads operations — an API server whose work is done by a separate
	// fleet leaves it entirely unset.
	Worker operations.WorkerConfig `env:",init" envPrefix:"WORKER_" json:"worker,omitzero" yaml:"worker,omitempty"`

	// Watcher configures the subscription loop. It is inert for a process that
	// serves no streaming endpoint.
	Watcher operations.WatcherConfig `env:",init" envPrefix:"WATCHER_" json:"watcher,omitzero" yaml:"watcher,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills unset knobs on every half with their package defaults,
// and derives the queue's name and notify channel from the operations config.
func (cfg *Config) EnsureDefaults() {
	cfg.Operations.EnsureDefaults()
	cfg.Worker.EnsureDefaults()
	cfg.Watcher.EnsureDefaults()

	// Derived, never configured. See the field comment.
	cfg.Queue.Name = cfg.Operations.QueueName

	cfg.Queue.EnsureDefaults()
}

// ValidateWithContext validates every half.
//
// Each nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Operations, validation.By(func(any) error {
			return cfg.Operations.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Worker, validation.By(func(any) error {
			return cfg.Worker.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Watcher, validation.By(func(any) error {
			return cfg.Watcher.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Queue, validation.By(func(any) error {
			return cfg.Queue.ValidateWithContext(ctx)
		})),
	)
}

// NewStore builds the operations store from configuration.
//
// client must speak Postgres: this package's SQL is written against it rather
// than reduced to a portable subset, and operations.NewSQLStore returns
// dialect.ErrUnsupported for anything else.
func NewStore(cfg *Config, client database.Client, opts ...Option) (operations.Store, error) {
	if cfg == nil {
		return nil, operations.ErrNilConfig
	}

	cfg.EnsureDefaults()

	o := newOptions(opts)

	base := []operations.StoreOption{
		operations.WithStoreTablePrefix(cfg.Operations.TablePrefix),
		operations.WithStoreNotifyChannel(cfg.Operations.NotifyChannel),
	}

	if o.logger != nil {
		base = append(base, operations.WithStoreLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, operations.WithStoreTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, operations.WithStoreMetricsProvider(o.metricsProvider))
	}

	store, err := operations.NewSQLStore(client, append(base, o.store...)...)
	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewQueue builds the work queue operations are dispatched through.
//
// It is exported because the queue is shared: the service enqueues onto it and
// the worker claims from it, and a second Queue over the same table would mean a
// second enqueue batcher — which is the part that only pays off when there is
// exactly one.
//
// A Queue owns a goroutine and must be Closed.
func NewQueue(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (*workqueue.Queue[string], error) {
	if cfg == nil {
		return nil, operations.ErrNilConfig
	}

	cfg.EnsureDefaults()

	o := newOptions(opts)

	base := make([]workqueue.Option, 0, len(o.queue)+4) //nolint:mnd // the four options below

	if o.logger != nil {
		base = append(base, workqueue.WithLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, workqueue.WithTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, workqueue.WithMetricsProvider(o.metricsProvider))
	}
	if o.queueWakeup != nil {
		base = append(base, workqueue.WithWakeup(o.queueWakeup))
	}

	return workqueue.New[string](ctx, &cfg.Queue, client, append(base, o.queue...)...)
}

// NewService builds the store, the queue, and the service over both, returning
// the service and the queue.
//
// The queue comes back because it owns a goroutine the caller has to Close, and
// because a process that also runs a worker hands the same value to NewWorker.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	registry *operations.Registry,
	opts ...Option,
) (operations.Service, *workqueue.Queue[string], error) {
	if cfg == nil {
		return nil, nil, operations.ErrNilConfig
	}

	store, err := NewStore(cfg, client, opts...)
	if err != nil {
		return nil, nil, err
	}

	queue, err := NewQueue(ctx, cfg, client, opts...)
	if err != nil {
		return nil, nil, err
	}

	svc, err := newServiceOver(ctx, cfg, store, queue, registry, opts...)
	if err != nil {
		// The queue owns a goroutine, and a constructor that failed after
		// building one has to give it back — otherwise a process that reports a
		// configuration error at boot also leaks a batcher for as long as it
		// takes somebody to notice.
		//
		//nolint:errcheck // the caller is already being handed the failure that
		// matters; a Close error on top of it has nowhere useful to go.
		_ = queue.Close(ctx)

		return nil, nil, err
	}

	return svc, queue, nil
}

// newServiceOver applies the option translation both service constructors share.
func newServiceOver(
	ctx context.Context,
	cfg *Config,
	store operations.Store,
	queue *workqueue.Queue[string],
	registry *operations.Registry,
	opts ...Option,
) (operations.Service, error) {
	o := newOptions(opts)

	base := make([]operations.ServiceOption, 0, len(o.service)+3) //nolint:mnd // the three options below

	if o.logger != nil {
		base = append(base, operations.WithLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, operations.WithTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, operations.WithMetricsProvider(o.metricsProvider))
	}

	svc, err := operations.NewService(ctx, &cfg.Operations, store, queue, registry, append(base, o.service...)...)
	if err != nil {
		return nil, err
	}

	return svc, nil
}

// NewWorker builds the run loop over an existing store and queue.
//
// Both are passed rather than built, because a worker process almost always
// starts operations too — the service that accepts an export request is commonly
// the one that performs it — and the queue in particular must be the same value
// the service enqueues onto.
func NewWorker(
	ctx context.Context,
	cfg *Config,
	store operations.Store,
	queue *workqueue.Queue[string],
	registry *operations.Registry,
	opts ...Option,
) (*operations.Worker, error) {
	if cfg == nil {
		return nil, operations.ErrNilConfig
	}

	cfg.EnsureDefaults()

	o := newOptions(opts)

	base := make([]operations.WorkerOption, 0, len(o.worker)+3) //nolint:mnd // the three options below

	if o.logger != nil {
		base = append(base, operations.WithWorkerLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, operations.WithWorkerTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, operations.WithWorkerMetricsProvider(o.metricsProvider))
	}

	return operations.NewWorker(ctx, &cfg.Worker, store, queue, registry, append(base, o.worker...)...)
}

// NewWatcher builds the subscription loop over an existing store.
//
// A Watcher owns a goroutine and must be Closed, and its Run must be started for
// any subscription to receive anything after its first snapshot.
func NewWatcher(
	ctx context.Context,
	cfg *Config,
	store operations.Store,
	opts ...Option,
) (*operations.Watcher, error) {
	if cfg == nil {
		return nil, operations.ErrNilConfig
	}

	cfg.EnsureDefaults()

	o := newOptions(opts)

	base := make([]operations.WatcherOption, 0, len(o.watcher)+4) //nolint:mnd // the four options below

	if o.logger != nil {
		base = append(base, operations.WithWatcherLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, operations.WithWatcherTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, operations.WithWatcherMetricsProvider(o.metricsProvider))
	}
	if o.watcherWakeup != nil {
		base = append(base, operations.WithWatcherWakeup(o.watcherWakeup))
	}

	return operations.NewWatcher(ctx, &cfg.Watcher, store, append(base, o.watcher...)...)
}

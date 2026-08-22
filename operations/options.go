package operations

import (
	"time"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// StoreOption configures a SQL Store at construction.
type StoreOption func(*SQLStore)

// WithStoreLogger attaches a logger to the store.
func WithStoreLogger(logger logging.Logger) StoreOption {
	return func(s *SQLStore) { s.logger = logger }
}

// WithStoreTracerProvider attaches a tracer provider to the store.
func WithStoreTracerProvider(tracerProvider tracing.Provider) StoreOption {
	return func(s *SQLStore) { s.tracerProvider = tracerProvider }
}

// WithStoreMetricsProvider attaches a metrics provider to the store. An absent
// provider records nothing.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) StoreOption {
	return func(s *SQLStore) { s.metricsProvider = metricsProvider }
}

// WithStoreTablePrefix namespaces the operations table. It must match the
// namespace the migrations were rendered with; nothing here can check that, and
// a mismatch surfaces as a missing table on the first query.
func WithStoreTablePrefix(prefix string) StoreOption {
	return func(s *SQLStore) { s.tables = newTables(prefix) }
}

// WithStoreNotifyChannel makes every write to an operation row emit a
// payload-free pg_notify on this channel.
//
// It is what turns the watch path from a poll into a push. Pair it with a
// pgnotify.Listener on the same channel, whose Signal feeds WithWatcherWakeup.
// Without it the watch path still delivers every state an operation passes
// through, a poll interval late.
func WithStoreNotifyChannel(channel string) StoreOption {
	return func(s *SQLStore) { s.notifyChannel = channel }
}

type (
	// ServiceOption configures a Service at construction.
	ServiceOption func(*serviceOptions)

	serviceOptions struct {
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider
	}
)

func newServiceOptions(opts []ServiceOption) *serviceOptions {
	o := &serviceOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger to the service.
func WithLogger(logger logging.Logger) ServiceOption {
	return func(o *serviceOptions) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider to the service.
func WithTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(o *serviceOptions) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider to the service. An absent
// provider records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(o *serviceOptions) { o.metricsProvider = metricsProvider }
}

type (
	// StartOption customizes one Start.
	//
	// These are per-call rather than per-service because each of them is a
	// property of the request that asked for the work, not of the process
	// serving it: whose operation this is, and how urgently it is wanted.
	StartOption func(*startOptions)

	startOptions struct {
		id       string
		owner    string
		delay    time.Duration
		priority int
	}
)

func newStartOptions(opts []StartOption) *startOptions {
	o := &startOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithOwner scopes the operation to whoever it belongs to.
//
// It is opaque to this package and compared only for equality. Any surface that
// lists operations for a request must set it — see Operation.Owner.
func WithOwner(owner string) StartOption {
	return func(o *startOptions) { o.owner = owner }
}

// WithPriority puts this operation ahead of the rest of the queue. Higher goes
// first.
//
// Re-enqueueing can only raise a priority, never lower it — that is the work
// queue's rule and this inherits it — so a hurried operation stays hurried.
func WithPriority(priority int) StartOption {
	return func(o *startOptions) { o.priority = priority }
}

// WithDelay holds the operation back before a worker may claim it, measured
// from the moment the row lands.
//
// The operation is durable and readable as StatePending throughout, which is the
// difference between this and not starting it yet: a client can be handed an ID
// for work that will begin in an hour.
func WithDelay(delay time.Duration) StartOption {
	return func(o *startOptions) { o.delay = delay }
}

// WithID sets the operation's ID rather than minting one.
//
// It is the idempotency seam. An ID derived from whatever the caller is acting
// on — a request ID, a hash of the parameters — makes a retried Start collide
// with the operation it is retrying, which is reported as ErrDuplicateOperation
// rather than starting the same export twice. Without it every Start is a new
// operation, which is the right default and the wrong one for a handler behind
// a client that retries.
func WithID(id string) StartOption {
	return func(o *startOptions) { o.id = id }
}

type (
	// WorkerOption configures a Worker at construction.
	WorkerOption func(*workerOptions)

	workerOptions struct {
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider
	}
)

func newWorkerOptions(opts []WorkerOption) *workerOptions {
	o := &workerOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithWorkerLogger attaches a logger to the worker.
func WithWorkerLogger(logger logging.Logger) WorkerOption {
	return func(o *workerOptions) { o.logger = logger }
}

// WithWorkerTracerProvider attaches a tracer provider to the worker.
func WithWorkerTracerProvider(tracerProvider tracing.Provider) WorkerOption {
	return func(o *workerOptions) { o.tracerProvider = tracerProvider }
}

// WithWorkerMetricsProvider attaches a metrics provider to the worker.
func WithWorkerMetricsProvider(metricsProvider metrics.Provider) WorkerOption {
	return func(o *workerOptions) { o.metricsProvider = metricsProvider }
}

type (
	// WatcherOption configures a Watcher at construction.
	WatcherOption func(*watcherOptions)

	watcherOptions struct {
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		wakeup <-chan struct{}
	}
)

func newWatcherOptions(opts []WatcherOption) *watcherOptions {
	o := &watcherOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithWatcherLogger attaches a logger to the watcher.
func WithWatcherLogger(logger logging.Logger) WatcherOption {
	return func(o *watcherOptions) { o.logger = logger }
}

// WithWatcherTracerProvider attaches a tracer provider to the watcher.
func WithWatcherTracerProvider(tracerProvider tracing.Provider) WatcherOption {
	return func(o *watcherOptions) { o.tracerProvider = tracerProvider }
}

// WithWatcherMetricsProvider attaches a metrics provider to the watcher.
func WithWatcherMetricsProvider(metricsProvider metrics.Provider) WatcherOption {
	return func(o *watcherOptions) { o.metricsProvider = metricsProvider }
}

// WithWatcherWakeup gives the watch loop a channel to re-read on, beside its
// poll interval. A receive means "some operation may have changed"; the loop
// runs the same re-read it would have run on its next poll.
//
// It is a bare channel because the watcher must not learn where the wake came
// from. database/postgres/pgnotify fills it from LISTEN/NOTIFY — pair it with
// WithStoreNotifyChannel on the writing side — but a test fills it by hand.
//
// The channel should coalesce — capacity one, non-blocking sends, as
// pgnotify.Listener.Signal does. WatcherConfig.MinReadInterval floors the rate
// regardless.
//
// Without one the watch path is a plain poll, and every guarantee it makes is
// unchanged: a subscriber still sees every state an operation reaches, because
// what it receives is a snapshot of the row rather than a stream of the changes
// to it.
func WithWatcherWakeup(wakeup <-chan struct{}) WatcherOption {
	return func(o *watcherOptions) { o.wakeup = wakeup }
}

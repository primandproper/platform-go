package operationscfg

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/workqueue"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally would make a caller that wanted none of
// the three name all three anyway, usually as noops.
//
// The WithXOptions members pass options through to the components themselves.
// They cannot be second variadics on the constructors: Go allows one per
// function, and that slot is what makes the observability optional. There are
// four of them because this one package builds four things, and a caller wiring
// a worker process has nothing to say about a watcher.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	queueWakeup   <-chan struct{}
	watcherWakeup <-chan struct{}

	store   []operations.StoreOption
	service []operations.ServiceOption
	worker  []operations.WorkerOption
	watcher []operations.WatcherOption
	queue   []workqueue.Option
}

// newOptions applies opts, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger. An absent logger logs nowhere — including the
// recovery sweep's report that it re-enqueued stranded operations, which has no
// caller to be returned to.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on the
// instrumented operations. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing — including the lost-lease counter, which is the only way to learn
// that operations are being run twice.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

// WithQueueWakeup gives the work queue's Wait a channel to return on, so a
// worker claims a fresh operation in milliseconds rather than on its next poll.
//
// It is a separate option from WithWatcherWakeup because the two want different
// channels even when both come from Postgres: one is fed by the queue's
// NotifyChannel and fires when work is enqueued, the other by the operations
// store's and fires when a row changes. Sharing one would wake every watcher on
// every enqueue and every worker on every progress flush.
func WithQueueWakeup(wakeup <-chan struct{}) Option {
	return func(o *options) { o.queueWakeup = wakeup }
}

// WithWatcherWakeup gives the watch loop a channel to re-read on. See
// WithQueueWakeup on why it is not the same channel.
func WithWatcherWakeup(wakeup <-chan struct{}) Option {
	return func(o *options) { o.watcherWakeup = wakeup }
}

// WithStoreOptions passes opts to the store, applied after the options derived
// from configuration — so a caller can override anything.
func WithStoreOptions(opts ...operations.StoreOption) Option {
	return func(o *options) { o.store = append(o.store, opts...) }
}

// WithServiceOptions passes opts to the service.
func WithServiceOptions(opts ...operations.ServiceOption) Option {
	return func(o *options) { o.service = append(o.service, opts...) }
}

// WithWorkerOptions passes opts to the worker.
func WithWorkerOptions(opts ...operations.WorkerOption) Option {
	return func(o *options) { o.worker = append(o.worker, opts...) }
}

// WithWatcherOptions passes opts to the watcher.
func WithWatcherOptions(opts ...operations.WatcherOption) Option {
	return func(o *options) { o.watcher = append(o.watcher, opts...) }
}

// WithQueueOptions passes opts to the work queue. It is how a custom key codec
// reaches a queue built from configuration, since a codec is a Go value the
// environment cannot name.
func WithQueueOptions(opts ...workqueue.Option) Option {
	return func(o *options) { o.queue = append(o.queue, opts...) }
}

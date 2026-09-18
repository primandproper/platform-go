/*
Package workqueuecfg assembles a work queue, and optionally the runner that
drains it, from environment configuration.

There is one dependency neither can be built without: a database.Client speaking
Postgres. The dialect is not configured here at all — it comes off the client, so
the SQL cannot disagree with the database it runs against.

Both constructors are generic over the key type, which the caller names at the
call site. That is the only part of a queue the environment cannot express: a key
is a Go type, so a config file has nothing to say about it. A handler is the
other, which is why NewRunner takes one positionally.

# Why there is no Config here

Every other config subpackage in this module declares its own Config, and this
one deliberately does not — it takes *workqueue.Config and
*workqueue.RunnerConfig directly.

Those types earn their place by being a different shape than any single leaf
config: cachecfg.Config selects a provider and carries a circuit breaker,
outboxcfg.Config pairs a message queue with a relay, webhookscfg.Config gathers a
worker, an HTTP client, and a breaker. timerscfg.Config is the nearest neighbor
and the instructive contrast: it wraps a set and the worker that fires it,
because NewWorker there builds both from one value.

Nothing here builds both. A queue is claimed against by processes that never
enqueue and enqueued onto by processes that never claim, so NewRunner takes the
Queue rather than the client — a wrapper would be a type whose two halves are
never needed by one constructor, and whose nesting every consumer pays for in
JSON and YAML. What a consumer that wants them under one key writes is the two
fields, in their own application config, with whatever env prefixes they want.

Resist adding one back for symmetry. A consumer that wants the queue under its
own key nests workqueue.Config in its application config the way uploadscfg.Config
nests objectstorage.Config.
*/
package workqueuecfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/workqueue"

	"github.com/primandproper/primitives-go/v2/database"
)

// NewQueue builds a Queue from configuration.
//
// client must speak Postgres: this package's SQL is written against it rather
// than reduced to a portable subset, and workqueue.New returns
// dialect.ErrUnsupported for anything else. See the workqueue package doc for
// which construct is the binding one.
//
// K is the key type the queue schedules work for, and is the caller's to name:
//
//	queue, err := workqueuecfg.NewQueue[OrderID](ctx, cfg, client)
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything — including the key codec, which configuration has no way to
// express.
//
// The config is defaulted and validated by workqueue.New rather than here, so
// there is one place that decides what a usable queue config is.
//
// The returned Queue owns a goroutine and must be Closed.
func NewQueue[K comparable](
	ctx context.Context,
	cfg *workqueue.Config,
	client database.Client,
	opts ...Option,
) (*workqueue.Queue[K], error) {
	o := newOptions(opts)

	base := make([]workqueue.Option, 0, len(o.queue)+3) //nolint:mnd // the three observability options below

	if o.logger != nil {
		base = append(base, workqueue.WithLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, workqueue.WithTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, workqueue.WithMetricsProvider(o.metricsProvider))
	}

	return workqueue.New[K](ctx, cfg, client, append(base, o.queue...)...)
}

// NewRunner builds the loop that drains a queue: claim, hand each item to
// handler, complete what worked and hand back what did not, extending the leases
// on running handlers for as long as they run.
//
// It takes the *workqueue.Queue rather than building one, for the reason
// workqueue.NewRunner gives: a process that drains a queue usually enqueues onto
// it too, and two Queue values over one table merge no enqueues and report two
// sets of metrics. Build the queue with NewQueue and hand it over.
//
// handler is positional because it is the one dependency a runner cannot be
// given any other way: it is the work.
//
// The config is defaulted and validated by workqueue.NewRunner rather than here,
// so there is one place that decides what a usable runner config is.
//
// The returned Runner starts nothing on its own. Run blocks; stop it by
// cancelling the context you hand it, which drains the batch it is holding.
func NewRunner[K comparable](
	ctx context.Context,
	cfg *workqueue.RunnerConfig,
	queue *workqueue.Queue[K],
	handler workqueue.Handler[K],
	opts ...Option,
) (*workqueue.Runner[K], error) {
	o := newOptions(opts)

	base := make([]workqueue.RunnerOption, 0, len(o.runner)+3) //nolint:mnd // the three observability options below

	if o.logger != nil {
		base = append(base, workqueue.WithRunnerLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, workqueue.WithRunnerTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, workqueue.WithRunnerMetricsProvider(o.metricsProvider))
	}

	return workqueue.NewRunner(ctx, cfg, queue, handler, append(base, o.runner...)...)
}

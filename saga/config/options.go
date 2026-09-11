package sagacfg

import (
	"github.com/primandproper/platform-go/v14/saga"

	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. Requiring them positionally made a caller that wanted none of the
// three name all three anyway, usually as noops.
//
// The idempotency manager and the event publisher are options for the same
// reason and were parameters until they were not: each is a dependency a
// legitimate deployment simply has none of, and a positional parameter made
// every one of those deployments spell a nil the constructor's own
// documentation had already predicted. Both apply to NewWorker; NewStore
// accepts the type and ignores every value of it, so one wiring site can pass
// the same options to both.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	manager   *idempotency.Manager[saga.StepResult]
	publisher saga.EventPublisher
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

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on the
// instrumented operations. An absent tracer provider traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
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

// WithWorkerIdempotency attaches the manager the Worker records step results
// through, so a step that already ran on a previous advance is replayed from its
// recorded result rather than executed again. Absent, a step whose instance is
// advanced twice runs twice — see saga.WithWorkerIdempotency. NewStore ignores
// it.
func WithWorkerIdempotency(manager *idempotency.Manager[saga.StepResult]) Option {
	return func(o *options) { o.manager = manager }
}

// WithWorkerEventPublisher attaches the publisher the Worker announces instance
// lifecycle transitions through — RegisterOutboxEventPublisher builds the one
// that writes them to the outbox. Absent, instances still advance and nothing
// outside the saga tables hears about it. NewStore ignores it.
func WithWorkerEventPublisher(publisher saga.EventPublisher) Option {
	return func(o *options) { o.publisher = publisher }
}

package http

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

type (
	// Option configures a [WebhookHandler] at construction.
	Option func(*options)

	options struct {
		resolver       ScopeResolver
		afterApply     AfterApply
		logger         logging.Logger
		tracerProvider tracing.Provider
	}
)

func newOptions(opts []Option) *options {
	o := &options{}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithScopeResolver supplies the function that says which tenant a delivery is
// for. It is required; see ErrNilScopeResolver and [ScopeResolver].
func WithScopeResolver(resolver ScopeResolver) Option {
	return func(o *options) { o.resolver = resolver }
}

// WithAfterApply supplies the function that writes what belongs beside a
// reconciled delivery — an audit entry, an outbox event — on the same
// transaction. See [AfterApply].
func WithAfterApply(afterApply AfterApply) Option {
	return func(o *options) { o.afterApply = afterApply }
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithPillars supplies the logger and tracer provider at once. This handler
// records no metrics of its own — the Syncer beneath it counts every delivery
// by what it did — so the pillars' metrics provider is not read.
//
// Options apply in order, so WithPillars(p) followed by WithLogger(nil) leaves
// this handler unlogged.
func WithPillars(pillars *observability.Pillars) Option {
	return func(o *options) {
		if pillars == nil {
			return
		}

		o.logger = pillars.Logger
		o.tracerProvider = pillars.TracerProvider
	}
}

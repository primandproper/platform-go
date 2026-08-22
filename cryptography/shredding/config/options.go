package shreddingcfg

import (
	"github.com/primandproper/platform-go/v13/cryptography/shredding"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures what this package builds.
type Option func(*options)

type options struct {
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	store []shredding.SQLStoreOption
	keys  []shredding.Option
}

func newOptions(opts []Option) *options {
	o := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) {
		o.logger = logger
	}
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) {
		o.tracerProvider = tracerProvider
	}
}

// WithMetricsProvider attaches a metrics provider.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) {
		o.metricsProvider = metricsProvider
	}
}

// WithPillars supplies logger, tracer provider, and metrics provider at once.
//
// Options apply in order, so a narrower option after this one still wins:
// WithPillars(p) followed by WithMetricsProvider(nil) leaves this unmetered.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) {
		logger, tracerProvider, metricsProvider := p.Deps()
		o.logger, o.tracerProvider, o.metricsProvider = logger, tracerProvider, metricsProvider
	}
}

// WithStoreOptions passes options through to the SQL store.
func WithStoreOptions(opts ...shredding.SQLStoreOption) Option {
	return func(o *options) {
		o.store = append(o.store, opts...)
	}
}

// WithKeysOptions passes options through to the Keys surface. It is how a
// Broadcaster gets attached, since building one needs a publisher this package
// cannot invent.
func WithKeysOptions(opts ...shredding.Option) Option {
	return func(o *options) {
		o.keys = append(o.keys, opts...)
	}
}

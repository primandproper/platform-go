package tableaccess

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the Manager this package constructs. The zero configuration
// works: an absent logger logs nowhere and an absent tracer provider traces
// nowhere.
//
// There is no metrics provider. Everything here is an administrative act — a
// role created, a database dropped, a grant issued — performed a handful of
// times over a deployment's life by something that already reports what it did.
// A counter over those is a number nobody has a question for; the span and the
// log line, which say which role and which database, are what somebody auditing
// the change actually wants.
type Option func(*options)

type options struct {
	logger         logging.Logger
	tracerProvider tracing.Provider
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
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithPillars supplies logger and tracer provider at once. Options apply in
// order, so a WithPillars followed by a narrower option wins for that component.
// The pillars' metrics provider is ignored — see Option.
func WithPillars(pillars *observability.Pillars) Option {
	return func(o *options) {
		o.logger, o.tracerProvider, _ = pillars.Deps()
	}
}

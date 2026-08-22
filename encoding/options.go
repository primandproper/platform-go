package encoding

import (
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the encoders this package constructs. The zero
// configuration works: an absent logger logs nowhere and an absent tracer
// provider traces nowhere.
type Option func(*options)

type options struct {
	logger         logging.Logger
	tracerProvider tracing.Provider
}

func newOptions(opts []Option) *options {
	cfg := &options{}
	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// encode and decode.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithPillars attaches a logger and tracer provider in one go, for the common
// case where a caller has already built them together. A nil Pillars attaches
// nothing.
//
// The metrics provider a Pillars also carries is dropped, because this package
// records no instruments: encoding is a translation between a value and bytes,
// and the thing worth counting is whatever asked for the translation. Callers
// that want an encode counted instrument the operation around it.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, _ = p.Deps() }
}

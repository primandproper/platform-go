package totp

import (
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the Verifier this package constructs. The zero
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
//
// Worth setting. A failed second factor is a security-relevant event — it is
// what a credential-stuffing run looks like once the password has already
// worked — and without a logger the only trace it leaves is whatever the caller
// chooses to do with the returned sentinel.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling spans on every
// operation.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

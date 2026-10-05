package recordingcfg

import (
	"github.com/primandproper/platform-go/v14/recording"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures how this package's constructors assemble what they build.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere and an
// absent tracer provider traces nowhere.
type Option func(*options)

// options collects what the options set.
type options struct {
	logger         logging.Logger
	tracerProvider tracing.Provider

	recorder []recording.Option
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

// WithMetricsProvider attaches a metrics provider. It is accepted so that this
// package's options read like every other config package's, and is unused: a
// Recorder registers no instruments, because the two halves it writes through
// meter themselves.
func WithMetricsProvider(metrics.Provider) Option {
	return func(*options) {}
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go, for the common case where a caller has already built them together. A nil
// Pillars attaches nothing.
//
// It is applied in order with the individual options, so a caller can hand over
// its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, _ = p.Deps() }
}

// WithRecorderOptions passes opts to NewRecorder, which applies them after the
// options it derives from configuration.
func WithRecorderOptions(opts ...recording.Option) Option {
	return func(o *options) { o.recorder = append(o.recorder, opts...) }
}

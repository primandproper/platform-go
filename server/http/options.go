package http

import (
	"github.com/primandproper/platform-go/v13/healthcheck"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// Option configures the server this package constructs. The zero configuration
// works: an absent logger logs nowhere and an absent tracer provider traces
// nowhere.
type Option func(*options)

type options struct {
	logger         logging.Logger
	tracerProvider tracing.Provider
	healthRegistry healthcheck.Registry
	serviceName    string
	versionRoute   bool
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

// WithTracerProvider attaches a tracer provider, enabling spans on served
// requests.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithServiceName names the server's logger and instrumentation scope. It
// matches the gRPC sibling's option of the same name.
func WithServiceName(serviceName string) Option {
	return func(o *options) { o.serviceName = serviceName }
}

// WithHealthRegistry mounts LivenessPath and ReadinessPath, backed by the given
// registry. It names the same registry the gRPC sibling's option of the same
// name takes, so both transports answer from one set of checkers rather than
// from two that can disagree.
//
// The routes are opted into rather than always mounted: a service that already
// serves its own probes at these paths would otherwise find them registered
// twice, which most muxes answer with a panic. A nil registry mounts nothing.
//
// They are registered while the server is being constructed, so a caller that
// installs global middleware on the router does so before building the server —
// which is the ordering routing.Backend already documents, since most muxes
// refuse middleware added after the first route.
func WithHealthRegistry(registry healthcheck.Registry) Option {
	return func(o *options) { o.healthRegistry = registry }
}

// WithVersionEndpoint mounts VersionPath, serving the build metadata the binary
// was stamped with.
//
// It is separate from WithHealthRegistry because it answers a different
// question and carries a different decision: the commit a process is running is
// useful to an operator and is also information about the deployment, so a
// service exposing it on a public listener says so rather than inheriting it.
func WithVersionEndpoint() Option {
	return func(o *options) { o.versionRoute = true }
}

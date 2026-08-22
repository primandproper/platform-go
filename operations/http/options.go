package http

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/operations"
)

// ErrNilOwnerResolver indicates handlers built without an OwnerResolver.
//
// It has no default, and that is the point. Every read this package serves is
// scoped to an owner, and a default of "no scoping" would make the safe wiring
// and the wiring that serves every tenant's operations to anyone look identical.
// A deployment that genuinely has no owners passes Unscoped, by name.
var ErrNilOwnerResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil operations owner resolver")

type (
	// Option configures the handlers at construction.
	Option func(*options)

	options struct {
		resolver       OwnerResolver
		watcher        *operations.Watcher
		logger         logging.Logger
		tracerProvider tracing.Provider

		basePath string
		tags     []string
	}
)

func newOptions(opts []Option) *options {
	o := &options{basePath: BasePath, tags: []string{"operations"}}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithOwnerResolver supplies the function that says whose operations a request
// may read. It is required; see ErrNilOwnerResolver.
func WithOwnerResolver(resolver OwnerResolver) Option {
	return func(o *options) { o.resolver = resolver }
}

// WithWatcher enables the server-sent-events endpoint.
//
// Without one, that endpoint is not registered and not documented, and the
// polling endpoints are unaffected. Polling an operation every couple of seconds
// is a complete implementation of the watch path; the stream is what saves the
// request per poll.
//
// The Watcher's Run must be started, or every subscription receives its first
// snapshot and then nothing.
func WithWatcher(watcher *operations.Watcher) Option {
	return func(o *options) { o.watcher = watcher }
}

// WithBasePath mounts the surface somewhere other than /operations.
//
// It changes the paths Accepted renders too, so a consumer's own 202 keeps
// pointing at the endpoints that actually exist.
func WithBasePath(basePath string) Option {
	return func(o *options) {
		if basePath != "" {
			o.basePath = basePath
		}
	}
}

// WithTags sets the OpenAPI tags on every registered operation, replacing the
// default of "operations".
func WithTags(tags ...string) Option {
	return func(o *options) { o.tags = tags }
}

// WithLogger attaches a logger.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

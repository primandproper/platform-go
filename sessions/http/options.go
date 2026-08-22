package http

import (
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

var (
	// ErrNilStore indicates NewManager was called without a session store. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil session store")
	// ErrNilCookieManager indicates NewManager was called without a cookie
	// manager. It wraps errors.ErrNilInputParameter, so a caller may check
	// either.
	ErrNilCookieManager = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil cookie manager")
)

type (
	// Option configures a Manager at construction.
	//
	// It carries no type parameter even though NewManager does: Go cannot infer
	// a type argument from a call's result type, so an Option[T] would force
	// every call site to spell the payload type out by hand forever.
	Option func(*options)

	options struct {
		logger         logging.Logger
		tracerProvider tracing.Provider

		cookieName string
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{cookieName: DefaultCookieName}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCookieName overrides the cookie a session identifier travels in.
//
// Renaming it on a deployed service signs everybody out: the old cookie is
// still in every browser and nothing reads it any more. An empty name is
// ignored rather than honored — a cookie has to have one.
func WithCookieName(name string) Option {
	return func(o *options) {
		if name != "" {
			o.cookieName = name
		}
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

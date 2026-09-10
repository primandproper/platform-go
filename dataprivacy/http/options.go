package http

import (
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/tracing"
)

var (
	// ErrNilService indicates handlers built without a dataprivacy Service. It
	// wraps errors.ErrNilInputParameter, so a caller may check either.
	//
	// It is declared here rather than in dataprivacy because it is a fact about
	// wiring this surface up, not about a privacy request. A sentinel in that
	// package is one internal/sentinelmatrix records a transport decision for,
	// and there is no transport answer to give for an error raised before the
	// first request.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy service")

	// ErrNilSubjectResolver indicates handlers built without a SubjectResolver.
	//
	// It has no default, and unlike operations/http's owner resolver there is no
	// name for going without one. Every route here is scoped to a subject, and a
	// surface serving every subject's privacy requests to whoever asks is not a
	// deployment shape — it is the export endpoint working on the wrong person.
	ErrNilSubjectResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil dataprivacy subject resolver")
)

type (
	// Option configures the handlers at construction.
	Option func(*options)

	options struct {
		resolver       SubjectResolver
		scopes         ScopeResolver
		logger         logging.Logger
		tracerProvider tracing.Provider

		basePath       string
		operationsPath string
		tags           []string
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		basePath:       BasePath,
		operationsPath: operationshttp.BasePath,
		scopes:         UnconfinedRequests,
		tags:           []string{"privacy"},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithSubjectResolver supplies the function that says whose privacy requests a
// request may submit, read, confirm and cancel. It is required; see
// ErrNilSubjectResolver.
func WithSubjectResolver(resolver SubjectResolver) Option {
	return func(o *options) { o.resolver = resolver }
}

// WithScopeResolver supplies the function that says which tenant's privacy
// requests a call is about. It defaults to UnconfinedRequests; see that function
// for why this one has a default where WithSubjectResolver does not.
func WithScopeResolver(resolve ScopeResolver) Option {
	return func(o *options) {
		if resolve != nil {
			o.scopes = resolve
		}
	}
}

// WithBasePath mounts the surface somewhere other than /privacy-requests.
func WithBasePath(basePath string) Option {
	return func(o *options) {
		if basePath != "" {
			o.basePath = basePath
		}
	}
}

// WithOperationsBasePath says where operations/http is mounted, for the paths a
// Receipt points progress at. It defaults to that package's own BasePath, which
// is the same constant its Accepted renders from, so the two agree unless a
// consumer moves one of them and not the other.
//
// It changes no route registered here. Nothing in this package serves progress;
// this is the address of the surface that does.
func WithOperationsBasePath(basePath string) Option {
	return func(o *options) {
		if basePath != "" {
			o.operationsPath = basePath
		}
	}
}

// WithTags sets the OpenAPI tags on every registered operation, replacing the
// default of "privacy".
func WithTags(tags ...string) Option {
	return func(o *options) { o.tags = tags }
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

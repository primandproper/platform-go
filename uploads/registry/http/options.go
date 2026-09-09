package http

import (
	"context"

	"github.com/primandproper/platform-go/v14/uploads/registry"

	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"
)

// ErrNilCallerResolver indicates a handler built without a CallerResolver.
//
// It has no default, and that is the point. The whole of this package is the
// guard, and the guard is two facts about whoever is asking; a default would
// have to invent both. Every wiring of this route is therefore a wiring that
// named where the caller comes from.
var ErrNilCallerResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil upload registry caller resolver")

// Caller is who is asking, as the consumer's authentication already resolved
// them.
//
// The two fields travel together because they are one question. A scope with no
// principal serves every object in the tenant to anybody inside it, and a
// principal with no scope serves an object to whoever happens to own a row of
// the same id in some other tenant. Resolving them from one place is what keeps
// them from being wired from two.
type Caller struct {
	// PrincipalID is whoever the consumer's authorization model calls a
	// principal — a user, a service account, an API key — spelled the same way
	// registry.Object.OwnerID is, since comparing the two is what OwnerOnly
	// does.
	PrincipalID string

	// Scope is the tenant the request is being made in. It binds the read, so
	// an object in another tenant is not refused — it is absent.
	//
	// tenancy.Global() for a single-tenant application, which is then exactly
	// what the read would have been without the column.
	Scope tenancy.Scope
}

type (
	// CallerResolver derives the caller a request is being made by.
	//
	// It takes a context rather than the request because that is where a
	// consumer's authentication middleware has already put the identity: the
	// session, the token's subject, the tenant. Returning an error fails the
	// request as a 500, since a resolver that could not answer has not said the
	// caller is unauthorized — it has said it does not know, and this package
	// is not the thing that decides what to do about that.
	CallerResolver func(ctx context.Context) (Caller, error)

	// Entitlement decides whether a caller may read an object, given the row.
	//
	// It returns the decision and, separately, whether it could be made. False
	// with a nil error is a refusal and becomes a 404 like an absence; a
	// non-nil error is an entitlement that could not be evaluated — a
	// permissions lookup that timed out — and becomes a 500, because answering
	// "no" to a question nobody managed to ask is how a caller loses access to
	// their own document during an outage.
	//
	// The default is OwnerOnly. A consumer whose objects hang off something
	// shared — the attachments on a ticket everyone assigned to it may read —
	// supplies its own, and gets the row to decide from: registry.Object
	// carries the owner, the scope, and the subject the object is attached to.
	Entitlement func(ctx context.Context, caller Caller, object *registry.Object) (bool, error)
)

// OwnerOnly entitles the object's owner and nobody else. It is the default, and
// it is the rule registry's own documentation states: the row is the access
// control, and the row names one owner.
//
// A caller with no principal is refused even against a row with no owner.
// registry.Object refuses to validate without an owner, so an empty OwnerID is
// a row that was written around that check rather than a row that means
// "anyone" — and reading it as "anyone" is the version of this comparison
// somebody writes to make a test pass.
func OwnerOnly(_ context.Context, caller Caller, object *registry.Object) (bool, error) {
	if caller.PrincipalID == "" || object.OwnerID == "" {
		return false, nil
	}

	return object.OwnerID == caller.PrincipalID, nil
}

type (
	// Option configures the handler at construction.
	Option func(*options)

	options struct {
		resolver    CallerResolver
		entitlement Entitlement

		logger         logging.Logger
		tracerProvider tracing.Provider

		basePath string
		tags     []string
	}
)

func newOptions(opts []Option) *options {
	o := &options{
		entitlement: OwnerOnly,
		basePath:    BasePath,
		tags:        []string{"uploads"},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCallerResolver supplies the function that says who is asking. It is
// required; see ErrNilCallerResolver.
func WithCallerResolver(resolver CallerResolver) Option {
	return func(o *options) { o.resolver = resolver }
}

// WithEntitlement replaces the owner comparison with the consumer's own rule.
//
// It is the seam for objects that hang off something shared, and it is a
// replacement rather than an addition: a consumer that means "the owner, or
// anyone on the ticket" writes both halves, because a package that OR-ed its
// default into whatever it was given could never be told to be stricter.
//
// The scope is not delegated with it. The read has already been bound to the
// caller's scope by the time this runs, so an Entitlement never sees another
// tenant's row and cannot accidentally entitle one.
//
// A nil Entitlement leaves OwnerOnly in place.
func WithEntitlement(entitlement Entitlement) Option {
	return func(o *options) {
		if entitlement != nil {
			o.entitlement = entitlement
		}
	}
}

// WithBasePath mounts the route somewhere other than /objects.
func WithBasePath(basePath string) Option {
	return func(o *options) {
		if basePath != "" {
			o.basePath = basePath
		}
	}
}

// WithTags sets the OpenAPI tags on the registered operation, replacing the
// default of "uploads".
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

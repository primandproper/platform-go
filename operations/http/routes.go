package http

import (
	"context"
	nethttp "net/http"
	"path"
	"strings"

	"github.com/primandproper/platform-go/v14/operations"

	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	httpx "github.com/primandproper/primitives-go/errors/http"
	"github.com/primandproper/primitives-go/filtering"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/routing"
	"github.com/primandproper/primitives-go/tenancy"
)

// o11yName scopes this package's spans and logger.
const o11yName = "operations_http"

// Default paths. They are exported because a consumer that mounts this surface
// under a prefix still has to build the Location header its own start endpoint
// returns, and deriving that from the same constants is what keeps the two
// agreeing.
const (
	// BasePath is where the collection is mounted.
	BasePath = "/operations"

	// EventsSuffix is appended to an operation's path for its event stream.
	EventsSuffix = "/events"

	// CancelSuffix is appended to an operation's path for its cancellation.
	CancelSuffix = "/cancel"
)

// pathParam is the name of the operation ID path parameter, and must match the
// `path:` tags on the input types below.
const pathParam = "operationID"

// Observability keys for this package.
const (
	operationIDKey = "operations.id"
	ownerKey       = "operations.owner"
	streamedKey    = "operations.snapshots_streamed"
)

// OwnerResolver derives the scope a request is entitled to read from.
//
// It takes a context rather than a request because that is where a consumer's
// authentication middleware has already put the identity — the session, the
// token's subject, the tenant — and because the typed handlers this package
// registers receive a context and nothing else. A resolver that needed the
// request would work for the event stream and not for anything else.
//
// Returning an error fails the request. What it returns otherwise is bound into
// every statement the request makes, so a resolver that returns the zero Scope
// fails the read at the driver rather than widening it — which is the failure
// mode worth having, and is why there is no spelling of "every tenant" here.
type OwnerResolver func(ctx context.Context) (tenancy.Scope, error)

// GlobalOwner is the OwnerResolver for a deployment that genuinely has no
// owners: every operation is in tenancy.Global(), and every reader reads it.
//
// It is the counterpart of leaving WithOwner off at Start, which is what puts an
// operation in the global scope in the first place — so a single-tenant
// deployment that names no owner anywhere behaves exactly as it did before
// tenancy was a column.
//
// It exists as a name rather than as the default so that "this deployment has no
// tenants" is something somebody wrote down. A single-tenant internal tool is a
// perfectly good reason to pass it; a multi-tenant API passing it is not a leak
// — the global scope matches only itself, so such a reader would see nothing
// rather than everything — but it is still a mistake, and one the wiring should
// show.
func GlobalOwner(context.Context) (tenancy.Scope, error) { return tenancy.Global(), nil }

// Handlers is the mountable operations read surface.
type Handlers struct {
	svc      operations.Service
	watcher  *operations.Watcher
	resolver OwnerResolver
	o11y     observability.Observer

	// codec renders every body this package writes by hand: the event-stream
	// frames, and the refusals that happen before the upgrade. The typed
	// endpoints do not go through it — routing encodes those, under its own
	// content negotiation.
	//
	// It is pinned to JSON rather than negotiated, and built once rather than
	// per frame. Pinned because the stream's contract is JSON snapshots and the
	// SSE framing is a text protocol: a client reading text/event-stream has no
	// way to be told the payloads inside it are now CBOR, and the binary content
	// types would not survive the newline normalization the framing does.
	codec encoding.Codec

	basePath string
	tags     []string
}

// New builds the handlers over a Service.
//
// resolver is required and has no default; see GlobalOwner and the package
// documentation for why. Without WithWatcher the event-stream endpoint is not
// registered at all — a subscription endpoint with nothing behind it would
// accept a connection and then say nothing forever, which is worse than a 404.
func New(svc operations.Service, opts ...Option) (*Handlers, error) {
	if svc == nil {
		return nil, operations.ErrNilService
	}

	o := newOptions(opts)

	if o.resolver == nil {
		return nil, ErrNilOwnerResolver
	}

	// This surface answers through errors/http, which maps the primitives and
	// nothing above them, so mounting it registers the mapping it answers with.
	// Without this an operation belonging to somebody else is a 500 rather than
	// the 404 read goes to the trouble of returning, and a subscription refused
	// for capacity is a 500 rather than a 429 the client can back off from.
	//
	// This is the one exception to the rule that the composition root registers
	// the domain tier, and it stays one: this was the only surface here that
	// both answered through errors/http and belonged to a package
	// errormappers.Register names. dataprivacy/http is a second one now and
	// registers nothing, because a mapper set assembled from whichever handlers
	// a binary happens to have constructed is not one a consumer can read off a
	// single call. links ships no transport at all, and sessions/http ships one
	// that never writes an error response, so neither has anywhere to make this
	// statement either.
	//
	// Here rather than in an init, because linking a package in is not a
	// decision and building these handlers is: a consumer that constructs them
	// has said it wants operation errors on the wire. errormappers.Register does
	// the same for a service that never mounts them. Registration is additive
	// and the registry stops at the first match, so a second Handlers costs one
	// comparison and answers identically.
	httpx.RegisterHTTPErrorMapper(operations.HTTPMapper)

	return &Handlers{
		svc:      svc,
		watcher:  o.watcher,
		resolver: o.resolver,
		codec:    encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(o.logger), encoding.WithTracerProvider(o.tracerProvider)),
		basePath: o.basePath,
		tags:     o.tags,
		o11y:     observability.NewObserver(o11yName, o.logger, o.tracerProvider),
	}, nil
}

// getInput is the read of one operation.
type getInput struct {
	ID string `path:"operationID"`
}

// cancelInput is the cancellation request. It carries no body: a cancellation
// says nothing beyond which operation, and a body would be a place for somebody
// to put a reason this package would then have to store and never read.
type cancelInput struct {
	ID string `path:"operationID"`
}

// listInput is the collection read.
//
// The filter fields are spelled out rather than embedding filtering.QueryFilter,
// because that type carries knobs — created-before, include-archived — that mean
// nothing here, and a generated client should not offer parameters the endpoint
// ignores.
type listInput struct {
	Cursor string `query:"cursor"`
	Kind   string `query:"kind"`
	State  string `query:"state"`
	Limit  uint16 `query:"limit"`
}

// Mount registers every route on the router, and is the shorthand for wanting
// the whole surface — which is the ordinary case.
//
// A consumer that wants some of it calls the individual methods instead. There
// is no route list to hand back for somebody else to mount: routing.Route is
// what registration returns, not a value that can be registered, and it cannot
// become one — routing.Get is generic over the handler's input and output types,
// so the typed registration has to happen where those types are still known,
// which is here. Splitting Mount is what that constraint allows.
//
// Call whichever of these you call before MountOpenAPI, so the spec the router
// serves includes them.
func (h *Handlers) Mount(r *routing.Router) []*routing.Route {
	routes := []*routing.Route{
		h.MountGet(r),
		h.MountList(r),
		h.MountCancel(r),
	}

	if events := h.MountEvents(r); events != nil {
		routes = append(routes, events)
	}

	return routes
}

// MountGet registers the read of one operation.
func (h *Handlers) MountGet(r *routing.Router) *routing.Route {
	return routing.Get(r, path.Join(h.basePath, "/{"+pathParam+"}"), h.get,
		routing.WithSummary("Read a long-running operation"),
		routing.WithDescription(
			"Returns the operation as it currently stands. `done` is false while it may still "+
				"change and true once it will not; `result` is present only on success and `error` "+
				"only on failure.",
		),
		routing.WithTags(h.tags...),
	)
}

// MountList registers the collection read.
func (h *Handlers) MountList(r *routing.Router) *routing.Route {
	return routing.Get(r, h.basePath, h.list,
		routing.WithSummary("List long-running operations"),
		routing.WithTags(h.tags...),
	)
}

// MountCancel registers the cancellation endpoint.
//
// It is the one route here that is not a read, and the most likely thing for a
// consumer to leave off: a deployment whose operations should run to completion
// mounts the other three and does not offer this one.
func (h *Handlers) MountCancel(r *routing.Router) *routing.Route {
	return routing.Post(r, path.Join(h.basePath, "/{"+pathParam+"}", CancelSuffix), h.cancel,
		routing.WithSummary("Request cancellation of a long-running operation"),
		routing.WithDescription(
			"Cancellation is a request rather than a kill. An operation that has not started is "+
				"cancelled outright; a running one stops when its runner next checks, and may "+
				"still succeed if it finishes first. Cancelling a finished operation returns it "+
				"unchanged.",
		),
		// 200 rather than 202. The response body is the operation as it now
		// stands, which is the whole answer to "what happened to my request" —
		// and for an operation that had not started, or one that was already
		// finished, the cancellation is complete by the time this returns.
		routing.WithResponseStatus(nethttp.StatusOK),
		routing.WithTags(h.tags...),
	)
}

func (h *Handlers) get(ctx context.Context, in getInput) (*operations.Operation, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(operationIDKey, in.ID))
	defer span.End()

	op, err := h.read(ctx, span, in.ID)
	if err != nil {
		return nil, span.Error(err, "reading operation")
	}

	return op, nil
}

func (h *Handlers) cancel(ctx context.Context, in cancelInput) (*operations.Operation, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(operationIDKey, in.ID))
	defer span.End()

	// Read first, under the scope. Cancel is a write reached by an ID and
	// nothing else — operations.Store.RequestCancel is machinery and takes no
	// scope, for the reason it states — so without this read it would be a way
	// to stop other people's work without ever being able to read it. The read
	// is what confines the write to a tenant, and it is the caller's to make.
	if _, err := h.read(ctx, span, in.ID); err != nil {
		return nil, span.Error(err, "cancelling operation")
	}

	op, err := h.svc.Cancel(ctx, in.ID)
	if err != nil {
		return nil, span.Error(err, "cancelling operation")
	}

	return op, nil
}

func (h *Handlers) list(
	ctx context.Context,
	in listInput,
) (*filtering.QueryFilteredResult[operations.Operation], error) {
	ctx, span := h.o11y.Begin(ctx)
	defer span.End()

	scope, err := h.scope(ctx, span)
	if err != nil {
		return nil, span.Error(err, "resolving operation owner")
	}

	listScope := &operations.ListScope{Kind: in.Kind}

	if in.State != "" {
		state := operations.State(in.State)
		if !state.Valid() {
			return nil, span.Error(
				platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "unknown operation state %q", in.State),
				"listing operations",
			)
		}

		listScope.States = []operations.State{state}
	}

	results, err := h.svc.List(ctx, scope, listScope, filterFrom(in))
	if err != nil {
		return nil, span.Error(err, "listing operations")
	}

	return results, nil
}

// filterFrom builds the shared query filter from the endpoint's own parameters.
func filterFrom(in listInput) *filtering.QueryFilter {
	filter := filtering.DefaultQueryFilter()

	if in.Cursor != "" {
		cursor := in.Cursor
		filter.Cursor = &cursor
	}

	if in.Limit > 0 {
		limit := in.Limit
		filter.MaxResponseSize = &limit
	}

	return filter
}

// scope resolves whose operations this request may see, and records it.
//
// It is one function rather than a call in each handler so that a handler cannot
// be written that reads without resolving one: there is no path to svc.Get or
// svc.List from here that does not come through a resolved scope.
func (h *Handlers) scope(ctx context.Context, span observability.Operation) (tenancy.Scope, error) {
	scope, err := h.resolver(ctx)
	if err != nil {
		return tenancy.Scope{}, err
	}

	span.Set(ownerKey, scope.String())

	return scope, nil
}

// read fetches an operation in the scope the request resolved to.
//
// There is no comparison here, and its absence is the point. The scope is bound
// into the statement, so a row belonging to somebody else is one the query does
// not return — reported as ErrOperationNotFound, which is also what an operation
// that does not exist reports. The two are the same answer on purpose: a 403 for
// an operation that exists and a 404 for one that does not is an oracle telling
// whoever is guessing IDs which of their guesses are real.
func (h *Handlers) read(
	ctx context.Context,
	span observability.Operation,
	id string,
) (*operations.Operation, error) {
	scope, err := h.scope(ctx, span)
	if err != nil {
		return nil, err
	}

	op, err := h.svc.Get(ctx, scope, id)
	if err != nil {
		return nil, err
	}

	return op, nil
}

// Accepted renders the 202 body a consumer's own start endpoint returns, with
// the URLs of the endpoints this package mounts.
//
// It is the one line that connects a consumer's typed start handler to this read
// surface:
//
//	routing.Post(r, "/exports", func(ctx context.Context, in exportForm) (*operationshttp.Acceptance, error) {
//	    op, err := svc.Start(ctx, "dataprivacy.export", in.request(), operations.WithOwner(tenancy.Of(userID(ctx))))
//	    if err != nil {
//	        return nil, err
//	    }
//
//	    return handlers.Accepted(op), nil
//	}, routing.WithResponseStatus(http.StatusAccepted))
//
// The status is the caller's to set, because it is their endpoint. 202 is the
// right one and this cannot enforce it.
func (h *Handlers) Accepted(op *operations.Operation) *Acceptance {
	if op == nil {
		return nil
	}

	base := path.Join(h.basePath, op.ID)

	return &Acceptance{
		Operation: op,
		Location:  base,
		Events:    base + EventsSuffix,
	}
}

// Acceptance is the body of a 202: the operation, and where to watch it.
//
// The URLs are relative and rooted at the mount point. A client resolves them
// against the request URL, which is the one thing that is correct behind every
// proxy, ingress, and path rewrite a deployment might put in front of this —
// where an absolute URL built from a configured hostname is correct behind
// exactly the ones somebody remembered to configure.
type Acceptance struct {
	// Operation is the operation as recorded, in StatePending.
	Operation *operations.Operation `json:"operation"`

	// Location is the path to poll.
	Location string `json:"location"`

	// Events is the path to subscribe to over server-sent events.
	Events string `json:"events"`
}

// eventsPattern renders the raw (untyped) path the stream is registered under.
//
// It strips routing's inline type annotation, because the Backend matches on
// plain patterns and only routing's typed registration understands "{id:uuid}".
func (h *Handlers) eventsPattern() string {
	return path.Join(h.basePath, "/{"+pathParam+"}", EventsSuffix)
}

// operationIDFromPath pulls the ID out of a request path for the stream handler,
// which is registered on the Backend and therefore does not go through routing's
// binding.
//
// It reads the path rather than the backend's own parameter API because there is
// no parameter API on the Backend seam — deliberately, since every mux library
// spells it differently — and the shape here is fixed: the ID is the segment
// before the events suffix.
func operationIDFromPath(requestPath string) string {
	trimmed := strings.TrimSuffix(strings.TrimSuffix(requestPath, "/"), EventsSuffix)

	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}

	return trimmed
}

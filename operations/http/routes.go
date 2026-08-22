package http

import (
	"context"
	nethttp "net/http"
	"path"
	"strings"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/routing"
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

// OwnerResolver derives the owner a request is entitled to read from.
//
// It takes a context rather than a request because that is where a consumer's
// authentication middleware has already put the identity — the session, the
// token's subject, the tenant — and because the typed handlers this package
// registers receive a context and nothing else. A resolver that needed the
// request would work for the event stream and not for anything else.
//
// Returning an error fails the request; returning an empty string scopes the
// read to operations that have no owner, which is what Unscoped does on purpose.
type OwnerResolver func(ctx context.Context) (string, error)

// Unscoped is the OwnerResolver for a deployment that genuinely has no owners:
// every reader may read every operation.
//
// It exists as a name rather than as the default so that "everyone may read
// every operation" is something somebody wrote down. A single-tenant internal
// tool is a perfectly good reason to pass it; a multi-tenant API passing it is a
// data leak, and the difference should be visible in the wiring.
func Unscoped(context.Context) (string, error) { return "", nil }

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
// resolver is required and has no default; see Unscoped and the package
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

	op, err := h.read(ctx, in.ID)
	if err != nil {
		return nil, span.Error(err, "reading operation")
	}

	return op, nil
}

func (h *Handlers) cancel(ctx context.Context, in cancelInput) (*operations.Operation, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(operationIDKey, in.ID))
	defer span.End()

	// Read first, under the ownership check. Cancel is a write, and a write
	// reached by an ID somebody guessed would be a way to stop other people's
	// work without ever being able to read it.
	if _, err := h.read(ctx, in.ID); err != nil {
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

	owner, err := h.resolver(ctx)
	if err != nil {
		return nil, span.Error(err, "resolving operation owner")
	}

	span.Set(ownerKey, owner)

	scope := &operations.ListScope{Owner: owner, Kind: in.Kind}

	if in.State != "" {
		state := operations.State(in.State)
		if !state.Valid() {
			return nil, span.Error(
				platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "unknown operation state %q", in.State),
				"listing operations",
			)
		}

		scope.States = []operations.State{state}
	}

	results, err := h.svc.List(ctx, scope, filterFrom(in))
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

// read fetches an operation and enforces ownership.
//
// A row belonging to somebody else is reported as ErrOperationNotFound, not as a
// permission failure. The two are the same answer on purpose: a 403 for an
// operation that exists and a 404 for one that does not is an oracle telling
// whoever is guessing IDs which of their guesses are real.
func (h *Handlers) read(ctx context.Context, id string) (*operations.Operation, error) {
	owner, err := h.resolver(ctx)
	if err != nil {
		return nil, err
	}

	op, err := h.svc.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	if op.Owner != owner {
		return nil, platformerrors.Wrapf(operations.ErrOperationNotFound, "operation %q", id)
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
//	    op, err := svc.Start(ctx, "dataprivacy.export", in.request(), operations.WithOwner(userID(ctx)))
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

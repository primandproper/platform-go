package http

import (
	"context"
	"errors"
	"io"
	nethttp "net/http"
	"path"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	httpx "github.com/primandproper/primitives-go/errors/http"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/routing"
	"github.com/primandproper/primitives-go/uploads"

	"github.com/swaggest/openapi-go/openapi3"
)

// o11yName scopes this package's spans and logger.
const o11yName = "uploads_registry_http"

// BasePath is where the route is mounted by default.
//
// It is exported because a consumer renders the URL of an object into a page it
// is building — an img element, a link, a JSON field — and deriving that from
// ObjectPath rather than from a string typed a second time is what keeps the
// two agreeing when the mount point moves.
const BasePath = "/objects"

// pathParam is the name of the object ID path parameter.
const pathParam = "objectID"

// serveOperationID is the OpenAPI operation ID of the route. It is named once
// and used twice — in the document and in the Route Mount returns — because
// those two are the same endpoint.
const serveOperationID = "serve_registered_object"

// The headers this package decides, spelled once. Each is a decision the
// package documentation argues for rather than a default it inherited.
const (
	cacheControlHeader = "Cache-Control"
	// cacheControlValue keeps every object this route serves out of shared
	// caches. A guard was consulted to produce the response, and a cache that
	// answered the next request would be answering it without one.
	cacheControlValue = "private"

	contentDispositionHeader = "Content-Disposition"
	contentTypeHeader        = "Content-Type"

	contentTypeOptionsHeader = "X-Content-Type-Options"
	contentTypeOptionsValue  = "nosniff"

	acceptRangesHeader = "Accept-Ranges"
	// acceptRangesNone is what a manager with no range capability advertises.
	// Saying "none" rather than saying nothing is the difference between a
	// client that asks once and a client that keeps asking.
	acceptRangesNone = "none"
)

// unknownContentType is what a row that does not say what its object is gets on
// the wire.
//
// It is written explicitly rather than left off, because http.ServeContent
// sniffs an absent one from the first bytes of the object — a read this package
// would then be doing to guess at something the row was supposed to say, and a
// guess whoever uploaded the bytes gets to steer. The object is served as an
// attachment either way; see dispositionFor.
const unknownContentType = "application/octet-stream"

// Observability keys.
//
// The object is labeled with registry's own exported attribute key rather than
// one spelled again here, which is what that constant is exported for: a span
// from this route and a span from the store beneath it name the same object the
// same way, so the two are one trace to query rather than two that resemble
// each other. The rest are about the request, which registry has no name for.
const (
	objectIDKey = mediaregistry.ObjectAttributeKey

	principalIDKey = o11yName + ".principal_id"
	scopeKey       = o11yName + ".scope"
	rangedKey      = o11yName + ".ranged"
)

// Handler is the guarded serve: one route that answers a read for one object.
//
// It is a binding rather than a resource surface — the package documentation
// says whose shape it stands in for and answers the README's objection to
// shipping handlers. What it holds is the three seams the guard needs: the rows
// to decide from, the reader to decide them on, and the bytes to hand over once
// the decision is made.
type Handler struct {
	store   mediaregistry.Store
	client  database.Client
	manager uploads.UploadManager

	resolver    CallerResolver
	entitlement Entitlement

	// codec renders the refusals, which are the only bodies this package writes
	// that are not an object. It is pinned to JSON rather than negotiated: a
	// client of this route asked for an image and is being told it cannot have
	// one, and the platform's error envelope is what every other refusal in a
	// service already looks like.
	codec encoding.Codec

	o11y observability.Observer

	basePath string
	tags     []string
}

// New builds the handler over a registry store, the client its reads run on,
// and the manager that holds the bytes.
//
// The three are parameters rather than options because none of them can be
// defaulted: a guard with no rows decides nothing, a guard with no reader
// cannot read them, and a serve with no bytes is a 404 generator. The client is
// here for Reader() alone — this route writes nothing, and holds no transaction
// for the same reason it needs none: one read, joined to nothing.
//
// A CallerResolver is required and has no default; see ErrNilCallerResolver.
// The Entitlement defaults to OwnerOnly, which is the rule registry's own
// documentation states.
func New(
	store mediaregistry.Store,
	client database.Client,
	manager uploads.UploadManager,
	opts ...Option,
) (*Handler, error) {
	switch {
	case store == nil:
		return nil, mediaregistry.ErrNilStore
	case client == nil:
		return nil, mediaregistry.ErrNilDatabaseClient
	case manager == nil:
		return nil, mediaregistry.ErrNilUploadManager
	}

	o := newOptions(opts)

	if o.resolver == nil {
		return nil, ErrNilCallerResolver
	}

	return &Handler{
		store:       store,
		client:      client,
		manager:     manager,
		resolver:    o.resolver,
		entitlement: o.entitlement,
		codec: encoding.NewClientEncoder(encoding.ContentTypeJSON,
			encoding.WithLogger(o.logger),
			encoding.WithTracerProvider(o.tracerProvider)),
		o11y:     observability.NewObserver(o11yName, o.logger, o.tracerProvider),
		basePath: o.basePath,
		tags:     o.tags,
	}, nil
}

// Mount registers the route and describes it in the OpenAPI document.
//
// The registration goes on the Backend rather than through routing's typed
// registration, for the same reason the operations event stream does: a typed
// handler is func(ctx, In) (Out, error) and the framework encodes what it
// returns, where this writes an object's bytes under the object's own content
// type. Expressing it approximately would generate a document promising a JSON
// body this endpoint never sends.
//
// The Route comes back hand-built, because the untyped registration has no
// operation to return.
func (h *Handler) Mount(r *routing.Router) *routing.Route {
	pattern := h.pattern()

	r.Handle(nethttp.MethodGet, pattern, nethttp.HandlerFunc(h.serve))

	h.describe(r)

	return &routing.Route{
		Method:      nethttp.MethodGet,
		Path:        pattern,
		OperationID: serveOperationID,
	}
}

// ObjectPath renders the path an object is served at, so a consumer building a
// page links to the route that is actually mounted rather than to a string
// typed twice.
//
// It is relative and rooted at the mount point. A client resolves it against
// the document it appears in, which is the one thing that is correct behind
// every proxy, ingress and path rewrite a deployment might put in front of
// this.
func (h *Handler) ObjectPath(objectID string) string {
	return path.Join(h.basePath, objectID)
}

// serve reads the row, checks the caller against it, and only then opens the
// bucket.
//
// The order is the whole package. Nothing touches storage until the row has
// said this caller may have the object, so a refusal costs no call to the
// bucket at all — which means a bucket that is unwell cannot turn one into a
// 500, and a 500 is a louder answer than 404: it says something was there.
func (h *Handler) serve(res nethttp.ResponseWriter, req *nethttp.Request) {
	ctx, span := h.o11y.Begin(req.Context())
	defer span.End()

	objectID := objectIDFromPath(req.URL.Path)

	span.Set(objectIDKey, objectID)

	object, err := h.read(ctx, span, objectID)
	if err != nil {
		h.refuse(ctx, res, span, err)

		return
	}

	h.write(ctx, res, req, span, object)
}

// read resolves the caller, reads the row in their scope, and asks whether they
// are entitled to it.
//
// Every refusal it makes is mediaregistry.ErrObjectNotFound, whatever produced it. A
// row in another tenant already reads as absent — the store binds the scope, so
// there is no row to refuse — and a row the entitlement declines is reported
// the same way on purpose: a 403 for an object that exists beside a 404 for one
// that does not is an oracle that tells whoever is guessing IDs which of their
// guesses are real.
//
// What is not collapsed into that answer is a failure to decide. A resolver
// that could not say who is calling, and an entitlement that could not evaluate
// itself, both come back as themselves and become a 500, because answering "no"
// to a question nobody managed to ask is how a caller loses access to their own
// document during an outage.
func (h *Handler) read(ctx context.Context, span observability.Operation, objectID string) (*mediaregistry.Object, error) {
	if objectID == "" {
		return nil, platformerrors.Wrap(mediaregistry.ErrObjectNotFound, "no object named")
	}

	caller, err := h.resolver(ctx)
	if err != nil {
		return nil, span.Error(err, "resolving the caller")
	}

	span.Set(principalIDKey, caller.PrincipalID)
	span.Set(scopeKey, caller.Scope.String())

	object, err := h.store.GetObject(ctx, h.client.Reader(), caller.Scope, objectID)
	if err != nil {
		return nil, span.Error(err, "reading the object's row")
	}

	// A Store that reports neither a row nor an error has said nothing, and
	// nothing is not an entitlement. It is treated as an absence rather than
	// passed to the Entitlement, which would be handed a nil row to decide
	// from — and the shortest rule that decides from one dereferences it.
	if object == nil {
		return nil, platformerrors.Wrapf(mediaregistry.ErrObjectNotFound, "object %q", objectID)
	}

	entitled, err := h.entitlement(ctx, caller, object)
	if err != nil {
		return nil, span.Error(err, "deciding the caller's entitlement")
	}

	if !entitled {
		return nil, platformerrors.Wrapf(mediaregistry.ErrObjectNotFound, "object %q", objectID)
	}

	return object, nil
}

// write serves the bytes, under the headers the row and this package decide
// between them.
//
// The row decides what the object is and how big it is; this package decides
// how it is presented, whether it may be cached, and whether the browser is
// allowed to disagree about the type. See the package documentation for why the
// last three are not configurable.
func (h *Handler) write(
	ctx context.Context,
	res nethttp.ResponseWriter,
	req *nethttp.Request,
	span observability.Operation,
	object *mediaregistry.Object,
) {
	contentType := object.ContentType
	if contentType == "" {
		contentType = unknownContentType
	}

	header := res.Header()
	header.Set(contentTypeHeader, contentType)
	header.Set(contentDispositionHeader, dispositionFor(object.ContentType))
	header.Set(contentTypeOptionsHeader, contentTypeOptionsValue)
	header.Set(cacheControlHeader, cacheControlValue)

	ranger, ok := h.manager.(uploads.RangeReader)

	span.Set(rangedKey, ok)

	if !ok {
		h.writeWhole(ctx, res, span, object)

		return
	}

	content := &rangedObject{ctx: ctx, reader: ranger, key: object.Key, size: object.Size}

	defer func() {
		if err := content.Close(); err != nil {
			span.Acknowledge(err, "closing the object reader")
		}
	}()

	// The name is only reached if the Content-Type header is unset, which it
	// never is by the time we are here. It is passed anyway so that the
	// fallback, if that ever stops being true, is the key's own extension
	// rather than a sniff of the object.
	//
	// The modification time is the row's rather than the bucket's, which is
	// what makes If-Modified-Since answerable without a second round trip. For
	// anything StoreAndRecord wrote the two are the same moment; for an object
	// registered against an existing bucket the row's time is when it was
	// registered, which is the only time this module knows.
	nethttp.ServeContent(res, req, path.Base(object.Key), modTimeOf(object), content)
}

// writeWhole serves the entire object, for a manager that cannot open a range.
//
// A Range header on such a request is ignored rather than answered, and
// Accept-Ranges says so. Emulating the range by opening at zero and discarding
// to the offset is refused: it is byte handling this package has no business
// doing, and it costs the transfer it claims to save.
//
// No Content-Length is written. The row's Size is a fact about what was
// counted going in, and on the ranged path it is what the response claims —
// but claiming it here, ahead of a copy that has not happened, is how a row
// that overstates its object becomes a truncated response rather than a short
// one. The chunked encoding net/http falls back to is correct whatever the
// bucket actually holds.
func (h *Handler) writeWhole(
	ctx context.Context,
	res nethttp.ResponseWriter,
	span observability.Operation,
	object *mediaregistry.Object,
) {
	res.Header().Set(acceptRangesHeader, acceptRangesNone)

	opened, err := h.manager.Open(ctx, object.Key)
	if err != nil {
		// Nothing has been written yet — only headers set — so this is still an
		// ordinary refusal rather than a truncated body.
		h.refuse(ctx, res, span, err)

		return
	}

	defer func() {
		if closeErr := opened.Close(); closeErr != nil {
			span.Acknowledge(closeErr, "closing the object reader")
		}
	}()

	if _, err = io.Copy(res, opened); err != nil {
		// The status is already sent, so there is no refusal to write: the
		// client sees a body that stopped. Worth a line, not worth an error —
		// a client that navigated away produces this too.
		span.Acknowledge(err, "writing the object")
	}
}

// refuse writes the error envelope, under the status this package decides.
//
// An absent object, an object in another tenant and an object the caller is not
// entitled to arrive here as one error and leave as one answer. That mapping is
// made here rather than through a registered HTTP error mapper: registry ships
// none, and adding one would put the module's metadata sentinels on a wire that
// carries no metadata — this route answers with an object or with this.
// Everything else goes through errors/http, which maps the primitives, and
// resolves to a 500 for the bucket failures that are the honest 500s.
func (h *Handler) refuse(ctx context.Context, res nethttp.ResponseWriter, span observability.Operation, err error) {
	status, body := httpx.ToAPIResponse(err)

	if errors.Is(err, mediaregistry.ErrObjectNotFound) {
		status, body = nethttp.StatusNotFound,
			httpx.NewAPIErrorResponse("object not found", httpx.ErrDataNotFound, httpx.ResponseDetails{})
	}

	encoded, err := h.codec.Marshal(ctx, body)
	if err != nil {
		nethttp.Error(res, "could not encode the response", nethttp.StatusInternalServerError)

		return
	}

	header := res.Header()

	// The headers the object would have carried are cleared. A refusal is not
	// the object, and a Content-Disposition of "inline" left over from a row
	// this caller may not read would describe a body they are not getting.
	header.Del(contentDispositionHeader)
	header.Del(acceptRangesHeader)
	header.Set(contentTypeHeader, h.codec.ContentType())
	header.Set(contentTypeOptionsHeader, contentTypeOptionsValue)
	header.Set(cacheControlHeader, cacheControlValue)

	res.WriteHeader(status)

	// The body is the platform's own error envelope, encoded by the platform's
	// own codec and served under the content type that codec names. There is no
	// HTML context for it to escape into.
	//nolint:gosec // G705: see above.
	if _, err = res.Write(encoded); err != nil {
		span.Acknowledge(err, "writing the refusal")
	}
}

// modTimeOf is the moment the response reports the object as last changed.
//
// LastUpdatedAt is nil for every row registry writes today — nothing it emits
// assigns a column after the insert — so this is the registration time in
// practice. It reads the field anyway, because the day a statement does assign
// one is not the day to discover that this header did not notice.
func modTimeOf(object *mediaregistry.Object) time.Time {
	if object.LastUpdatedAt != nil {
		return *object.LastUpdatedAt
	}

	return object.CreatedAt
}

// pattern renders the raw path the route is registered under. It carries no
// routing type annotation, because the Backend matches on plain patterns.
func (h *Handler) pattern() string {
	return path.Join(h.basePath, "/{"+pathParam+"}")
}

// objectIDFromPath pulls the ID out of a request path.
//
// It reads the path rather than the backend's parameter API because there is no
// parameter API on the Backend seam — deliberately, since every mux library
// spells it differently — and the shape here is fixed: the ID is whatever
// follows the last separator.
//
// A path that ends in one therefore names no object, and is left to say so
// rather than being trimmed into naming the segment before it. The route is
// registered as basePath/{objectID}, so a request that reaches this has already
// matched something in that position; reading a trailing slash as the previous
// segment would be this function deciding what a request meant.
func objectIDFromPath(requestPath string) string {
	if idx := strings.LastIndex(requestPath, "/"); idx >= 0 {
		return requestPath[idx+1:]
	}

	return requestPath
}

// describe writes the route into the OpenAPI document by hand.
//
// Routing's reflector builds an operation from a typed handler's input and
// output, and this endpoint has neither in the shape it understands. Left out,
// the document would not mention the one route this package ships.
func (h *Handler) describe(r *routing.Router) {
	spec := r.Spec()
	if spec == nil {
		return
	}

	description := "Returns the bytes of a registered object, under the content type its row records. " +
		"The caller is checked against the owner and the scope on that row rather than against " +
		"knowledge of the object's key. An object that does not exist, one belonging to another " +
		"tenant, and one the caller is not entitled to are all answered with 404."

	op := openapi3.Operation{
		Tags:        h.tags,
		ID:          new(serveOperationID),
		Summary:     new("Read a registered object"),
		Description: &description,

		Parameters: []openapi3.ParameterOrRef{{
			Parameter: &openapi3.Parameter{
				Name:     pathParam,
				In:       openapi3.ParameterInPath,
				Required: new(true),
				Schema: &openapi3.SchemaOrRef{
					Schema: &openapi3.Schema{Type: new(openapi3.SchemaTypeString)},
				},
			},
		}}}

	op.Responses.MapOfResponseOrRefValues = map[string]openapi3.ResponseOrRef{
		"200": {
			Response: &openapi3.Response{
				Description: "The object's bytes.",
				Content: map[string]openapi3.MediaType{
					unknownContentType: {},
				},
			},
		},
	}

	// The reflector's own error accumulator is not reachable from here, so a
	// failure to add this is logged rather than swallowed: the route still
	// works, and the document is missing one entry.
	if err := spec.AddOperation(nethttp.MethodGet, h.pattern(), op); err != nil {
		h.o11y.Logger().Error("describing the object serve in the OpenAPI document", err)
	}
}

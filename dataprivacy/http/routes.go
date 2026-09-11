package http

import (
	"context"
	nethttp "net/http"
	"path"

	"github.com/primandproper/platform-go/v14/dataprivacy"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// o11yName scopes this package's spans and logger.
const o11yName = "dataprivacy_http"

// Default paths. They are exported because the confirmation link a consumer's
// mail carries is built outside this package — the Notifier writes the message —
// and deriving that URL from the same constants is what keeps the link and the
// route agreeing.
const (
	// BasePath is where the collection is mounted.
	BasePath = "/privacy-requests"

	// ConfirmSuffix is appended to a request's path for its confirmation.
	ConfirmSuffix = "/confirm"

	// CancelSuffix is appended to a request's path for its withdrawal.
	CancelSuffix = "/cancel"
)

// pathParam is the name of the request ID path parameter, and must match the
// `path:` tags on the input types below.
const pathParam = "requestID"

// Observability keys for this package. They carry the dataprivacy. prefix the
// package below uses, because they are the same facts seen from the edge, and a
// trace that spans both should show one attribute from two vantage points rather
// than two attributes about one thing.
//
// Nothing here records anything collected about the subject. The rule is
// dataprivacy's and it does not relax at the transport: a span exporter is
// durable storage the subject never consented to.
const (
	requestIDKey   = "dataprivacy.request_id"
	requestTypeKey = "dataprivacy.request_type"
	subjectIDKey   = "dataprivacy.subject_id"
	statusKey      = "dataprivacy.status"
)

// SubjectResolver derives the subject a request acts for.
//
// It takes a context rather than an *http.Request because that is where a
// consumer's authentication middleware has already put the identity — the
// session, the token's subject — and because the typed handlers this package
// registers receive a context and nothing else.
//
// Returning an error fails the request. Returning a subject with no ID fails it
// too, as dataprivacy.ErrEmptySubjectID: there is no such thing as an unscoped
// read here, and a resolver that yields nobody has not decided that everyone may
// read everything, it has failed to say who is asking.
type SubjectResolver func(ctx context.Context) (dataprivacy.Subject, error)

// ScopeResolver derives the confinement a call is about.
//
// It is separate from SubjectResolver because the two answer different
// questions and a deployment answers them from different places: who is asking
// comes off the session, and which tenant they are asking about comes off the
// route, the host, or the membership the session was established under. They
// used to be one value, and a subject carrying the scope its own reads were
// narrowed by is how a listing ends up scoped by whatever assembled the struct.
//
// A nil scope narrows nothing: every confinement the subject appears in, which
// is what a person asking after their own privacy requests means. A non-nil one
// naming nobody is refused by the store rather than widened — see
// dataprivacy.Store.List, whose three readings this pointer is.
//
// On a submission there is no third reading, and nil is the request that names
// no confinement.
type ScopeResolver func(ctx context.Context) (*tenancy.Scope, error)

// UnconfinedRequests is the ScopeResolver a surface uses when a consumer names
// none: every call is unconfined, so a submission records no confinement and a
// read narrows by none.
//
// This one has a default where the subject resolver does not, and the asymmetry
// is the point rather than an oversight. A surface that does not know who is
// asking serves one person's export to another, which is the failure this
// package exists to prevent. A surface that does not know which tenant is being
// asked about serves a subject their own requests across all of them — which is
// the right answer for a single-tenant deployment and for the ordinary "give me
// my data", and never crosses to another person. Every route here is already
// narrowed to the resolved subject, so the widest thing this default can do is
// show somebody all of their own history.
func UnconfinedRequests(context.Context) (*tenancy.Scope, error) {
	//nolint:nilnil // nil is the answer here, not an absent one: it is the scope that narrows nothing.
	return nil, nil
}

// Handlers is the mountable data-privacy request surface.
type Handlers struct {
	svc      dataprivacy.Service
	resolver SubjectResolver
	scopes   ScopeResolver
	o11y     observability.Observer

	basePath       string
	operationsPath string
	tags           []string
}

// New builds the handlers over a Service.
//
// resolver is required and has no default; see ErrNilSubjectResolver and the
// package documentation for why.
//
// Nothing is registered with the error registries here. dataprivacy.HTTPMapper
// is installed by errormappers.Register at the composition root, and without
// that call every sentinel this surface returns arrives as a 500. See the
// package documentation for why this surface does not follow operations/http
// onto its own registration.
func New(svc dataprivacy.Service, opts ...Option) (*Handlers, error) {
	if svc == nil {
		return nil, ErrNilService
	}

	o := newOptions(opts)

	if o.resolver == nil {
		return nil, ErrNilSubjectResolver
	}

	return &Handlers{
		svc:            svc,
		resolver:       o.resolver,
		scopes:         o.scopes,
		basePath:       o.basePath,
		operationsPath: o.operationsPath,
		tags:           o.tags,
		o11y:           observability.NewObserver(o11yName, o.logger, o.tracerProvider),
	}, nil
}

// submitInput is the submission. The subject is deliberately not on it: it comes
// from the SubjectResolver, and a field here would be the request asking to be
// performed on somebody else.
type submitInput struct {
	// Type is "export" or "erasure". An unrecognized one is refused as
	// dataprivacy.ErrUnknownRequestType, which the mapper answers 400 — it is
	// not silently narrowed to a default, because the two things this package
	// does are not substitutes for one another.
	Type string `json:"type"`
}

// requestInput names one request, and is what the three per-request routes bind.
type requestInput struct {
	ID string `path:"requestID"`
}

// listInput is the collection read.
//
// The filter fields are spelled out rather than embedding filtering.QueryFilter,
// because that type carries knobs — created-before, include-archived — that mean
// nothing here, and a generated client should not offer parameters the endpoint
// ignores.
type listInput struct {
	Cursor string `query:"cursor"`
	Limit  uint16 `query:"limit"`
}

// Receipt is what every route that answers about one request returns: the
// request as it now stands, and where the answer to "how is it going" arrives.
//
// The progress paths are this package's whole answer to the absence of a status
// endpoint. They are relative and rooted at the operations mount point, so a
// client resolves them against the request URL — which is the one thing that is
// correct behind every proxy, ingress and path rewrite a deployment might put in
// front of this, where an absolute URL built from a configured hostname is
// correct behind exactly the ones somebody remembered to configure.
type Receipt struct {
	// Request is the request as it now stands.
	Request *dataprivacy.Request `json:"request"`

	// Progress is the path to poll for how the work is going, and Events the
	// path to subscribe to over server-sent events. Both are operations/http's,
	// against Request.OperationID.
	//
	// They are empty exactly while there is no operation — an erasure awaiting
	// confirmation, which is the one state in which nothing is running. A client
	// that finds them empty has been told to wait for a person rather than for a
	// worker.
	Progress string `json:"progress,omitempty"`
	Events   string `json:"events,omitempty"`
}

// receipt renders the response for one request.
func (h *Handlers) receipt(req *dataprivacy.Request) *Receipt {
	if req == nil {
		return nil
	}

	out := &Receipt{Request: req}

	if req.OperationID != "" {
		base := path.Join(h.operationsPath, req.OperationID)
		out.Progress = base
		out.Events = base + operationshttp.EventsSuffix
	}

	return out
}

// Mount registers every route on the router, and is the shorthand for wanting
// the whole surface — which is the ordinary case.
//
// A consumer that wants some of it calls the individual methods instead; see the
// package documentation for the deployment that mounts four of these five and
// renders its own confirmation page.
//
// Call whichever of these you call before MountOpenAPI, so the spec the router
// serves includes them.
func (h *Handlers) Mount(r *routing.Router) []*routing.Route {
	return []*routing.Route{
		h.MountSubmit(r),
		h.MountList(r),
		h.MountGet(r),
		h.MountConfirm(r),
		h.MountCancel(r),
	}
}

// MountSubmit registers the submission endpoint.
func (h *Handlers) MountSubmit(r *routing.Router) *routing.Route {
	return routing.Post(r, h.basePath, h.submit,
		routing.WithSummary("Submit a data-privacy request"),
		routing.WithDescription(
			"Records an export or erasure request for the calling subject and starts the work "+
				"that fulfills it. An erasure submitted to a service with a confirmation window "+
				"comes back `awaiting_confirmation` with no progress paths, and nothing runs "+
				"until it is confirmed.",
		),
		// 202 rather than 201. The request is recorded and readable the moment
		// this returns, and nothing it asked for has happened yet — an export
		// that has not run has produced no resource to have created. It is also
		// the honest answer to the gap the submission cannot close: the row and
		// the operation commit together, but the enqueue that follows cannot
		// join them, so a request submitted at exactly the wrong moment waits
		// for the operations recovery sweep rather than for a worker. Recorded
		// and readable throughout, which is what 202 says.
		routing.WithResponseStatus(nethttp.StatusAccepted),
		routing.WithTags(h.tags...),
	)
}

// MountList registers the collection read, scoped to the calling subject.
func (h *Handlers) MountList(r *routing.Router) *routing.Route {
	return routing.Get(r, h.basePath, h.list,
		routing.WithSummary("List the calling subject's data-privacy requests"),
		routing.WithDescription(
			"A subject is entitled to know what has been asked in their name, which is why this "+
				"is scoped to one rather than global.",
		),
		routing.WithTags(h.tags...),
	)
}

// MountGet registers the read of one request.
func (h *Handlers) MountGet(r *routing.Router) *routing.Route {
	return routing.Get(r, path.Join(h.basePath, "/{"+pathParam+"}"), h.get,
		routing.WithSummary("Read one data-privacy request"),
		routing.WithDescription(
			"Returns what was asked, by whom, when the response is owed, and whether the "+
				"artifact still exists. How far along the work is lives at the `progress` path, "+
				"which is the operations surface against this request's operation.",
		),
		routing.WithTags(h.tags...),
	)
}

// MountConfirm registers the confirmation endpoint.
//
// It is a GET because it is reached by clicking a link in a mail, and a link
// click is a GET. That is a state change on a verb that does not promise one, and
// the package documentation says what it costs and what a deployment that wants
// a human click does instead — which is to leave this one route unmounted.
func (h *Handlers) MountConfirm(r *routing.Router) *routing.Route {
	return routing.Get(r, path.Join(h.basePath, "/{"+pathParam+"}", ConfirmSuffix), h.confirm,
		routing.WithSummary("Confirm a data-privacy request"),
		routing.WithDescription(
			"Moves an erasure out of `awaiting_confirmation` and starts the work that fulfills "+
				"it. A request in any other state — already confirmed, already cancelled, or "+
				"one whose window has lapsed — is a conflict rather than a second confirmation.",
		),
		routing.WithTags(h.tags...),
	)
}

// MountCancel registers the withdrawal endpoint.
func (h *Handlers) MountCancel(r *routing.Router) *routing.Route {
	return routing.Post(r, path.Join(h.basePath, "/{"+pathParam+"}", CancelSuffix), h.cancel,
		routing.WithSummary("Withdraw a data-privacy request"),
		routing.WithDescription(
			"An unconfirmed erasure is cancelled outright. A request already in progress has "+
				"its operation asked to stop, which is a request rather than a kill: the "+
				"response says `in_progress` because that is what it still is, and the answer "+
				"arrives at the `progress` path.",
		),
		// 200 rather than 202, and the body is the request as it now stands. For
		// an unconfirmed erasure the withdrawal is complete by the time this
		// returns; for one in progress the response says so by still reading
		// in_progress rather than by a status code promising less.
		routing.WithResponseStatus(nethttp.StatusOK),
		routing.WithTags(h.tags...),
	)
}

func (h *Handlers) submit(ctx context.Context, in submitInput) (*Receipt, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(requestTypeKey, in.Type))
	defer span.End()

	subject, scope, err := h.caller(ctx)
	if err != nil {
		return nil, span.Error(err, "submitting dataprivacy request")
	}

	span.Set(subjectIDKey, subject.ID)

	// A nil scope is the submission that names no confinement, which is the
	// zero tenancy.Scope. The read paths keep the pointer because they have a
	// third reading to tell apart; a write does not.
	var confinement tenancy.Scope
	if scope != nil {
		confinement = *scope
	}

	req, err := h.svc.Submit(ctx, confinement, subject, dataprivacy.RequestType(in.Type))
	if err != nil {
		return nil, span.Error(err, "submitting dataprivacy request")
	}

	span.Set(requestIDKey, req.ID).Set(statusKey, string(req.Status))

	return h.receipt(req), nil
}

func (h *Handlers) list(
	ctx context.Context,
	in listInput,
) (*filtering.QueryFilteredResult[dataprivacy.Request], error) {
	ctx, span := h.o11y.Begin(ctx)
	defer span.End()

	subject, scope, err := h.caller(ctx)
	if err != nil {
		return nil, span.Error(err, "listing dataprivacy requests")
	}

	span.Set(subjectIDKey, subject.ID)

	results, err := h.svc.List(ctx, scope, subject, filterFrom(in))
	if err != nil {
		return nil, span.Error(err, "listing dataprivacy requests")
	}

	return results, nil
}

func (h *Handlers) get(ctx context.Context, in requestInput) (*Receipt, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(requestIDKey, in.ID))
	defer span.End()

	req, _, err := h.read(ctx, in.ID)
	if err != nil {
		return nil, span.Error(err, "reading dataprivacy request")
	}

	return h.receipt(req), nil
}

func (h *Handlers) confirm(ctx context.Context, in requestInput) (*Receipt, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(requestIDKey, in.ID))
	defer span.End()

	_, scope, err := h.read(ctx, in.ID)
	if err != nil {
		return nil, span.Error(err, "confirming dataprivacy request")
	}

	req, err := h.svc.Confirm(ctx, scope, in.ID)
	if err != nil {
		return nil, span.Error(err, "confirming dataprivacy request")
	}

	span.Set(statusKey, string(req.Status))

	return h.receipt(req), nil
}

func (h *Handlers) cancel(ctx context.Context, in requestInput) (*Receipt, error) {
	ctx, span := h.o11y.Begin(ctx, observability.WithValue(requestIDKey, in.ID))
	defer span.End()

	_, scope, err := h.read(ctx, in.ID)
	if err != nil {
		return nil, span.Error(err, "cancelling dataprivacy request")
	}

	req, err := h.svc.Cancel(ctx, scope, in.ID)
	if err != nil {
		return nil, span.Error(err, "cancelling dataprivacy request")
	}

	span.Set(statusKey, string(req.Status))

	return h.receipt(req), nil
}

// subject resolves who is asking, and refuses a resolver that named nobody.
func (h *Handlers) subject(ctx context.Context) (dataprivacy.Subject, error) {
	subject, err := h.resolver(ctx)
	if err != nil {
		return dataprivacy.Subject{}, err
	}

	if subject.ID == "" {
		return dataprivacy.Subject{}, platformerrors.Wrap(
			dataprivacy.ErrEmptySubjectID, "resolving the dataprivacy subject of a request",
		)
	}

	return subject, nil
}

// read fetches a request, enforces that it is the caller's, and hands back the
// scope its writes should run under.
//
// The confinement is no longer compared here: it is bound into the statement,
// because dataprivacy.Store.Get takes the scope it selects by and a request in
// another tenant is reported absent rather than fetched and rejected. What is
// still compared here is the subject, which no store method narrows by — a
// request names one person, and this surface serves each person their own.
//
// That comparison is an ownership guard standing in front of two writes as well
// as a read, which is normally not a check at all: it reads through one
// connection what the write will act on through another. What makes it one here
// is that the fact being checked cannot change. A request's subject is written
// once, by the insert — dataprivacy.Store.Save does not update, and none of the
// statements that move a request between statuses touches the subject — so
// there is no interleaving in which the second connection sees a different
// answer. The guard the writes genuinely need is the status one, and that is
// bound into their own statements a layer down.
func (h *Handlers) read(ctx context.Context, requestID string) (*dataprivacy.Request, *tenancy.Scope, error) {
	subject, scope, err := h.caller(ctx)
	if err != nil {
		return nil, nil, err
	}

	req, err := h.svc.Get(ctx, scope, requestID)
	if err != nil {
		return nil, nil, err
	}

	if req == nil || subject.ID != req.Subject.ID {
		return nil, nil, platformerrors.Wrapf(dataprivacy.ErrRequestNotFound, "dataprivacy request %q", requestID)
	}

	return req, scope, nil
}

// caller resolves who is asking and which confinement they are asking about.
//
// The two are resolved together because every route needs both and neither is
// derivable from the other. A failure in either fails the request: a surface
// that could not decide whose data this is has not decided that everyone may
// read everything.
func (h *Handlers) caller(ctx context.Context) (dataprivacy.Subject, *tenancy.Scope, error) {
	subject, err := h.subject(ctx)
	if err != nil {
		return dataprivacy.Subject{}, nil, err
	}

	scope, err := h.scopes(ctx)
	if err != nil {
		return dataprivacy.Subject{}, nil, platformerrors.Wrap(err, "resolving the dataprivacy scope of a request")
	}

	return subject, scope, nil
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

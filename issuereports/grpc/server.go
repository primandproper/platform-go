package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/logging"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/observability/tracing"
	"github.com/primandproper/primitives-go/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// serverName scopes this surface's spans, logger and instruments.
const serverName = "issuereports_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey       = "issuereports.scope"
	userIDKey      = "issuereports.user_id"
	reportIDKey    = "issuereports.report_id"
	reporterKey    = "issuereports.reporter"
	statusKey      = "issuereports.status"
	fromStatusKey  = "issuereports.from_status"
	subjectTypeKey = "issuereports.subject_type"
	subjectIDKey   = "issuereports.subject_id"
)

// The wiring failures this surface refuses to be built with.
var (
	// ErrNilStore is a server built over no store.
	//
	// There is no service between this package and the store, because there is
	// no orchestration to put in one: every RPC here is one call plus its
	// conversion, and the two that are more are more only in the way a read-back
	// is more.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports store")

	// ErrNilDatabaseClient is a server built with no handle to read on and no way
	// to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the issue reports gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal files reports in a name nobody typed and hands one
	// tenant's queue to anybody who can reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports principal extractor")

	// ErrNilReportAuthorizer is a server built with no rule about whose reports a
	// caller may name. See [ReportAuthorizer] for why there is no default.
	ErrNilReportAuthorizer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports authorizer")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the issue reports request context")

	// ErrNilReportInput is a create or a revision that named no report at all.
	//
	// It is this package's own rather than issuereports.ErrNilReport, because the
	// two are different facts: that one is a nil argument inside the process,
	// which is a bug, and this one is a request whose input field was never set,
	// which is a client to correct.
	ErrNilReportInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "issue report request names no report")
)

var _ issuereportspb.IssueReportsServiceServer = (*Server)(nil)

// Server is the gRPC surface over the report queue.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, what each method requires is a default
// map a consumer composes into their own, and whose reports a caller may name is
// a rule this package asks rather than answers — see [Permissions], [Require]
// and [ReportAuthorizer].
type Server struct {
	issuereportspb.UnimplementedIssueReportsServiceServer

	store      issuereports.Store
	client     database.Client
	principals PrincipalExtractor
	targets    ReportAuthorizer
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a store.
//
// The database client is two things here rather than one. It is what the reads
// run on, Reader(), outside any transaction, because a read on this surface has
// nothing to join. And it is what opens the transaction the writes need:
// issuereports' writes take a database.Tx, which only Client.WithTransaction
// produces, so an RPC handler — a caller with genuinely nothing of its own to
// join — is exactly the caller that method's documentation describes.
//
// The principal extractor and the authorizer are positional, and neither has a
// default; see [ErrNilPrincipalExtractor] and [ReportAuthorizer].
func NewServer(
	store issuereports.Store,
	client database.Client,
	principals PrincipalExtractor,
	targets ReportAuthorizer,
	opts ...Option,
) (*Server, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	if targets == nil {
		return nil, ErrNilReportAuthorizer
	}

	s := &Server{store: store, client: client, principals: principals, targets: targets}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating issuereports grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting the report queue
// beside the directory is one more entry in the slice that constructor already
// takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, reportsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	issuereportspb.RegisterIssueReportsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the operation
// to record on, whose queue the request is against, and who is asking.
//
// The principal itself is carried rather than only its scope, because two things
// need it. CreateReport writes the caller's identifier into Report.Reporter, so
// a report's authorship comes off the connection rather than out of a request
// field, and the two RPCs whose target is a person or somebody's row hand the
// whole caller to the [ReportAuthorizer] — a consumer's rule is theirs to write,
// and narrowing it to a user id here would decide in advance what it may read.
type request struct {
	op        observability.Operation
	principal Principal
	scope     tenancy.Scope
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than five lines per method because the five can be got
// wrong separately — an RPC that forgot to count an attempt leaves a latency
// histogram with no denominator, one that forgot to end the span leaks it, one
// that read no principal answers about somebody else's queue. The returned func
// is deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
//
// The scope comes off the principal and never off a request field. There is no
// ScopeResolver here, unlike authentication/signin/grpc: a caller reaching this
// surface has already become a principal, and a tenant a client could name would
// be a cross-tenant read hiding behind a request field — which on this surface
// means reading what other people's users said about their product.
func (s *Server) caller(ctx context.Context, method string) (
	context.Context, *request, func(err error), error,
) {
	ctx, op := s.o11y.Begin(ctx)

	attr := operationAttr(method)
	s.instruments.Attempt(ctx, attr)

	stop := op.Time(ctx, nil, s.instruments.Latency, attr)

	done := func(err error) {
		if err != nil {
			s.instruments.Failed(ctx, attr)
		}

		stop()
		op.End()
	}

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		err := grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipal, op.Logger(), op.Span(),
			codes.Unauthenticated, "resolving the caller of %s", method)

		// The RPC returns before it has deferred done, so this failure closes
		// what it opened itself — otherwise every anonymous request is a span
		// never ended and a failure nobody counted.
		done(err)

		return ctx, nil, func(error) {}, err
	}

	scope := principal.Scope()

	op.Set(scopeKey, scope.String())
	op.Set(userIDKey, principal.UserID())

	return ctx, &request{op: op, scope: scope, principal: principal}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("issuereports.rpc", method))
}

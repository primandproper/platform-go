package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/audit/auditpb"

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

// serverName scopes this package's spans, logger and instruments.
const serverName = "audit_grpc"

// The keys this package attaches to spans and log lines. They are audit's own,
// so a trace crossing the transport boundary does not carry the same fact under
// two names.
const (
	scopeKey   = "audit.scope"
	entryIDKey = "audit.entry_id"
)

// The errors this package returns for its own failures, as opposed to the
// reader's.
var (
	// ErrNilReader indicates a nil audit.Reader. Every RPC here is one call
	// into it, so there is no server that can be built without one.
	ErrNilReader = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil audit reader")

	// ErrNilScopeResolver indicates a [Server] built without a ScopeResolver —
	// [WithScopeResolver] absent, or given nil.
	//
	// It is refused at construction rather than defaulted, and this is the one
	// place this service diverges from authentication/signin/grpc, which
	// defaults to the global scope. There the failure a default produces is a
	// sign-in that refuses everybody, which is loud. Here it would be a console
	// reading the platform chain and reporting almost nothing — an audit log
	// that answers "no entries" looks like a quiet system rather than like a
	// broken one, and it is the one wrong answer in this package that nobody
	// investigates. A single-tenant deployment names [GlobalScope] and has said
	// so.
	//
	// Being an option rather than a positional argument changes the spelling and
	// not the policy: an option with nothing behind it refuses a server that
	// named none exactly as a nil argument did.
	ErrNilScopeResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil scope resolver for the audit server")
)

// Server is AuditService over an audit.Reader.
//
// # What it is
//
// Three RPCs, each one call into the reader with a conversion on either side:
// one entry by id, a page of them, and a verification of the caller's chain.
// There is no orchestration here and nothing to orchestrate — the reader owns
// its own handle and runs on the read replica, so this type holds no
// database.Client and opens no transaction.
//
// # What it is not
//
// It is strictly narrower than the interface beneath it, in two places, and
// both are the point of the surface rather than omissions from it.
//
// audit.Recorder is absent. A recording belongs inside the transaction of the
// change it describes, and an RPC would put it in this service's instead — see
// the interface's own documentation, which states the reason the whole
// transport lane's rule is derived from.
//
// audit.Reader.Get takes an id and no scope, because in a process it is an
// operator's read and the operator chose it. Here the entry's own scope is
// compared against the connection's, and one belonging to somebody else reads
// as absent — which is what it is from here. audit.Query.Scope is likewise not
// reachable: [ScopeResolver] fills it in on every list, and the schema reserves
// the field name so a client has nothing to send.
//
// It holds no policy. What each RPC requires is [Permissions], a default
// fragment a consumer composes into its own authorization policy and enforces
// with authorization/grpc's interceptor before a method here runs.
//
// # Errors
//
// A method here hands the reader's error back with a default code and does not
// switch on sentinels. What a missing entry means on the wire is decided once,
// by audit.GRPCMapper, and a switch here would be a second copy of that
// decision free to drift from it.
//
// That mapper reaches a client only once it is registered, which is
// errormappers.Register — one call, made by service.Register for a service
// built from a service.Config and by a hand-assembled service itself. This
// constructor deliberately does not make it: a mapper that installs itself by a
// component being constructed is a process-wide side effect a consumer cannot
// opt out of.
type Server struct {
	auditpb.UnimplementedAuditServiceServer

	reader audit.Reader
	scopes ScopeResolver

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	instruments *metrics.OperationSet
}

var _ auditpb.AuditServiceServer = (*Server)(nil)

// NewServer builds the gRPC surface over an audit reader.
//
// It takes the reader rather than a database.Client, which is the dependency
// shape this domain has and no other one in the lane does: reading the log is
// the half that owns a handle, and the half that does not is not on this
// surface at all. It takes the audit.Reader seam rather than the *audit.SQLReader
// a consumer will usually hand it, so that a consumer whose log lives behind
// their own implementation gets the same surface — and so that this package's
// own tests can drive the scope binding without a database.
//
// The reader is positional and the scope resolver is a required option:
// [WithScopeResolver] has no default behind it, so a server built without it is
// refused with [ErrNilScopeResolver] exactly as a nil positional argument was.
// The refusal is the policy; the spelling is what the rest of the transport
// lane does, and audit is no longer the one surface where the seam is passed a
// different way.
func NewServer(reader audit.Reader, opts ...Option) (*Server, error) {
	if reader == nil {
		return nil, ErrNilReader
	}

	s := &Server{reader: reader}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if s.scopes == nil {
		return nil, ErrNilScopeResolver
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating audit grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting the audit log
// beside the directory is two entries in the slice that constructor already
// takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, auditSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	auditpb.RegisterAuditServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, and whose log this is.
type request struct {
	op    observability.Operation
	scope tenancy.Scope
}

// begin opens the span, counts the attempt, starts the latency timer and
// resolves the scope, in the one place every RPC starts.
//
// It is one helper rather than four lines per method because the four can be
// got wrong separately — an RPC that forgot to count an attempt leaves a
// latency histogram with no denominator, one that forgot to end the span leaks
// it, and one that resolved no scope reads a chain that is not the caller's.
// The returned func is deferred by the caller and is called with the error the
// RPC is returning, which is why every early return below assigns err before
// returning it.
func (s *Server) begin(ctx context.Context, method string) (
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

	scope, err := s.scopes(ctx)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "resolving the log %s is against", method)

		// The RPC returns before it has deferred done, so this failure closes
		// what it opened itself — otherwise every unplaceable request is a span
		// never ended and a failure nobody counted.
		done(err)

		return ctx, nil, func(error) {}, err
	}

	// Validated here rather than left to the reader, because two of the three
	// RPCs would otherwise carry an unset scope into a comparison instead of
	// into a query: a resolver that answered the zero Scope without an error
	// would make GetEntry compare against "no scope" and match the platform
	// chain's entries.
	if err = scope.Validate(); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "resolving the log %s is against", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	op.Set(scopeKey, scope.String())

	return ctx, &request{op: op, scope: scope}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("audit.rpc", method))
}

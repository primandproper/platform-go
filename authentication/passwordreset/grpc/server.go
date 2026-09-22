package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// serverName scopes this package's spans, logger and instruments.
const serverName = "passwordreset_grpc"

// scopeKey is the key this package attaches to spans and log lines. It matches
// the one identity, signin and their transports use, so a trace crossing the
// boundary does not carry the same fact under two names.
const scopeKey = "identity.scope"

// The errors this package returns for its own failures, as opposed to the
// service's.
var (
	// ErrNilService indicates a nil *passwordreset.Service. Every RPC here goes
	// through it, so there is no server that can be built without one.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password reset service")
)

// Server is the gRPC surface over a password reset service.
//
// It holds no policy. How long a link lives, what the request floor is, which
// engine hashes the password and how the mail is sent are the service's options,
// and the service documents each. Whose directory a request is against is a
// [ScopeResolver] the consumer supplies. Neither is here.
//
// There is no principal extractor, and its absence is the shape of the service
// rather than an omission: all three RPCs are for somebody who cannot sign in,
// so there is no caller any of them could read. That is also why this package
// ships no authorizer seam, which every resource surface in this module that has
// a caller does.
//
// # Errors
//
// See the package documentation: the codes come from passwordreset.GRPCMapper,
// which a consumer registers through errormappers.Register, and this constructor
// deliberately does not register it.
type Server struct {
	passwordresetpb.UnimplementedPasswordResetServiceServer

	svc    *passwordreset.Service
	scopes ScopeResolver

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	instruments *metrics.OperationSet
}

var _ passwordresetpb.PasswordResetServiceServer = (*Server)(nil)

// NewServer builds the gRPC surface over a password reset service.
//
// The service is the one positional dependency, because it is the only one with
// no default: the scope resolver is an option and defaults to [GlobalScope],
// which is the single-tenant answer, and a multi-tenant deployment names one.
func NewServer(svc *passwordreset.Service, opts ...Option) (*Server, error) {
	if svc == nil {
		return nil, ErrNilService
	}

	s := &Server{svc: svc, scopes: GlobalScope}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating password reset grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting the reset flow
// beside sign-in is two entries in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{signInSrv.RegisterOn, resetSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	passwordresetpb.RegisterPasswordResetServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the operation
// to record on, and whose directory this is.
type request struct {
	op    observability.Operation
	scope tenancy.Scope
}

// anonymous is where all three RPCs start: the span, the instruments and the
// scope.
//
// It is one helper rather than four lines per method because the four can be got
// wrong separately — an RPC that forgot to count an attempt leaves a latency
// histogram with no denominator, one that forgot to end the span leaks it, one
// that resolved no scope reads the global directory. The returned func is
// deferred by the caller and is called with the error the RPC is returning, which
// is why every early return below assigns err before returning it.
//
// Every RPC here uses it, unlike signin/grpc's namesake, because there is no
// authenticated half of this service to need a second one.
func (s *Server) anonymous(ctx context.Context, method string) (
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
		err = grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.InvalidArgument, "resolving the directory %s is against", method)

		// The RPC returns before it has deferred done, so this failure closes
		// what it opened itself — otherwise every unplaceable request is a span
		// never ended and a failure nobody counted.
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
	return metric.WithAttributes(attribute.String("passwordreset.rpc", method))
}

package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

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
const serverName = "signin_grpc"

// The keys this package attaches to spans and log lines. They match the ones
// identity and signin use, so a trace crossing the transport boundary does not
// carry the same fact under two names.
const (
	scopeKey  = "identity.scope"
	userIDKey = "identity.user_id"
)

// The errors this package returns for its own failures, as opposed to the
// service's.
var (
	// ErrNilService indicates a nil *signin.Service. Every RPC here goes
	// through it, so there is no server that can be built without one.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil sign-in service")

	// ErrNilPrincipalExtractor indicates a nil PrincipalExtractor.
	//
	// It is refused at construction rather than defaulted, because the only
	// default available is one that resolves nobody — and the four RPCs that
	// need a caller would then refuse every request while the three that do not
	// kept working, which is a server that looks half alive.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor for the sign-in server")

	// ErrNoPrincipal indicates a request to one of the four authenticated RPCs
	// that arrived with nobody on it.
	//
	// It is a sentinel of its own rather than a wrap of a platform one, for the
	// reason identity/grpc's namesake gives: the module has no platform sentinel
	// for "unauthenticated", since authenticating is the consumer's. Every RPC
	// here answers it with codes.Unauthenticated at the call site, so it needs
	// no mapper.
	ErrNoPrincipal = platformerrors.New("no principal on the sign-in service's request context")

	// ErrNilCredentials indicates a sign-in request with no credentials message
	// on it. Answered with codes.InvalidArgument at the call site.
	ErrNilCredentials = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil credentials on a sign-in request")
)

// Server is SignInService over signin.Service.
//
// # What it is
//
// Seven RPCs, each one call into the service and a conversion on either side.
// Three are anonymous — the two doors, and the status read that answers "no" to
// a caller who is not signed in — and four require a principal.
//
// # What it is not
//
// It holds no policy. Whether a user without a second factor may sign in, how
// long a token lives, what it carries, and whether the administrative door
// exists at all are the service's options, and the service documents each. Who
// is calling is a [Principal] the consumer's own authentication interceptor put
// on the context, and whose directory the request is against is a
// [ScopeResolver] the consumer supplies. None of the three is here.
//
// It permissions nothing, and [Require] says so to an authorization policy
// explicitly rather than by omission — see that function for why the difference
// matters.
//
// # Errors
//
// See the package documentation: the codes come from signin.GRPCMapper, which a
// consumer registers through errormappers.Register, and this constructor
// deliberately does not register it.
type Server struct {
	signinpb.UnimplementedSignInServiceServer

	svc        *signin.Service
	principals PrincipalExtractor
	scopes     ScopeResolver

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	instruments *metrics.OperationSet
}

var _ signinpb.SignInServiceServer = (*Server)(nil)

// NewServer builds the gRPC surface over a sign-in service.
//
// It takes no database client, unlike identity/grpc: every RPC here is one call
// into the service, and there is no read on this surface that runs outside what
// the service already does. The two positional dependencies are the ones there
// is no default for — see [ErrNilPrincipalExtractor] for the extractor.
//
// The scope resolver is an option and defaults to [GlobalScope], which is the
// single-tenant answer. A multi-tenant deployment names one.
func NewServer(svc *signin.Service, principals PrincipalExtractor, opts ...Option) (*Server, error) {
	if svc == nil {
		return nil, ErrNilService
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	s := &Server{
		svc:        svc,
		principals: principals,
		scopes:     GlobalScope,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating sign-in grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting sign-in beside
// the directory is two entries in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, signInSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	signinpb.RegisterSignInServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, whose directory this is, and — for the four that
// require one — who is calling.
//
// It is a value rather than four return parameters because the helpers below
// would otherwise hand back six things each, which is the point at which a
// caller starts getting the order wrong.
type request struct {
	op        observability.Operation
	principal Principal
	scope     tenancy.Scope
}

// anonymous is where the three RPCs that need no caller start: the span, the
// instruments and the scope.
//
// It is one helper rather than four lines per method because the four can be
// got wrong separately — an RPC that forgot to count an attempt leaves a
// latency histogram with no denominator, one that forgot to end the span leaks
// it, one that resolved no scope reads the global directory. The returned func
// is deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
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

// caller is anonymous plus the principal the four authenticated RPCs need.
//
// The scope comes off the resolver rather than off the principal, so one wiring
// decision governs the whole service — see [ScopeResolver].
func (s *Server) caller(ctx context.Context, method string) (
	context.Context, *request, func(err error), error,
) {
	ctx, req, done, err := s.anonymous(ctx, method)
	if err != nil {
		return ctx, nil, done, err
	}

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipal, req.op.Logger(), req.op.Span(), codes.Unauthenticated, "resolving the caller of %s", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	req.principal = principal
	req.op.Set(userIDKey, principal.UserID())

	return ctx, req, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("signin.rpc", method))
}

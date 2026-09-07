package grpc

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	platformerrors "github.com/primandproper/platform-go/v14/errors"
	grpcerrors "github.com/primandproper/platform-go/v14/errors/grpc"
	"github.com/primandproper/platform-go/v14/observability"
	"github.com/primandproper/platform-go/v14/observability/logging"
	"github.com/primandproper/platform-go/v14/observability/metrics"
	"github.com/primandproper/platform-go/v14/observability/tracing"
	"github.com/primandproper/platform-go/v14/tenancy"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		err = fail(op, err, codes.InvalidArgument, "resolving the directory %s is against", method)

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
		err = fail(req.op, ErrNoPrincipal, codes.Unauthenticated, "resolving the caller of %s", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	req.principal = principal
	req.op.Set(userIDKey, principal.UserID())

	return ctx, req, done, nil
}

// rpcError is a handler failure that answers to both idioms: the sentinel chain
// for errors.Is, and a gRPC status for status.Code.
//
// It has to be both, and neither of the two functions named
// PrepareAndLogGRPCStatus produces both. observability's takes the code it is
// handed and flattens the chain into a status message — it cannot consult the
// mappers, since observability sits below errors/grpc and importing it back
// would be a cycle. errors/grpc's maps the code correctly and then calls
// observability's, so it flattens the chain too.
//
// Flattening is what breaks the wire. UnaryErrorEncodingInterceptor's job is to
// put the sentinel chain into the status details so a client's errors.Is
// matches, and it can only encode the chain it is given — a handler that already
// turned ErrInvalidCredentials into a string hands it a string.
//
// identity/grpc carries the same three-method type for the same reason, and this
// is the second copy. Promoting it into errors/grpc would delete both and fix
// every other caller of the recommended spelling at once; that is a change to
// errors/grpc rather than one to make from here, and it is why this type is
// unexported in both places.
type rpcError struct {
	err  error
	msg  string
	code codes.Code
}

func (e *rpcError) Error() string { return e.err.Error() }

func (e *rpcError) Unwrap() error { return e.err }

func (e *rpcError) GRPCStatus() *status.Status { return status.New(e.code, e.msg) }

// fail is how every RPC here returns an error: it logs and traces, then hands
// back an error that is still the sentinel it was.
//
// The code is a default rather than an answer. UnaryErrorEncodingInterceptor
// re-runs MapToGRPC over the chain this preserves, so a registered mapper wins
// over what a call site guessed; the code here is what a client is told when no
// mapper claims the error. That is why every method below passes
// codes.Internal for the service's own failures and does not switch on
// sentinels — deciding that a wrong password is Unauthenticated is
// signin.GRPCMapper's job, in one place.
//
// The message is the same shape. The description is what a client is told when
// nothing better is registered, and a client-safe sentinel's own words are
// better — which matters more here than anywhere else in the module, because
// four of this service's refusals share PermissionDenied and three share
// FailedPrecondition, and a client in a language that cannot read the encoded
// details has only the message to tell them apart.
func fail(
	op observability.Operation,
	err error,
	defaultCode codes.Code,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	description := fmt.Sprintf(descriptionFmt, descriptionArgs...)

	op.Acknowledge(err, "%s", description)

	msg := description
	if safe, ok := grpcerrors.ClientSafeMessage(err); ok {
		msg = safe
	}

	return &rpcError{
		err:  platformerrors.Wrap(err, description),
		msg:  msg,
		code: grpcerrors.MapToGRPC(err, defaultCode),
	}
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("signin.rpc", method))
}

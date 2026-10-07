package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/passkeys"
	"github.com/primandproper/platform-go/v15/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/callers"

	"github.com/primandproper/primitives-go/v2/database"
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
const serverName = "passkeys_grpc"

// The keys this package attaches to spans and log lines. They match the ones
// passkeys and signin use, so a trace crossing the transport boundary does not
// carry one fact under two names.
const (
	scopeKey  = "passkeys.scope"
	userIDKey = "passkeys.user_id"
)

// CredentialKind is what a passkey sign-in is stamped with on the
// signin.Authentication its hooks are handed, so an audit trail can tell one
// from a password.
const CredentialKind signin.CredentialKind = "passkey"

// The errors this package returns for its own failures, as opposed to the
// service's.
var (
	// ErrNilService indicates a nil *passkeys.Service.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil passkey service for the passkeys server")

	// ErrNilDatabaseClient indicates a nil database.Client. The ceremonies'
	// reads run on its reader and their writes in a transaction it opens.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the passkeys server")

	// ErrNilIssuer indicates a nil PrincipalIssuer. A finished login with
	// nothing to mint its token is a login the server could not answer.
	ErrNilIssuer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal issuer for the passkeys server")

	// ErrNilPrincipalExtractor indicates a nil callers.PrincipalExtractor,
	// refused for the reason authentication/signin/grpc refuses its own.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor for the passkeys server")

	// ErrNoPrincipal indicates a request to one of the self-service RPCs that
	// arrived with nobody on it. Answered with codes.Unauthenticated at the call
	// site, so it needs no mapper.
	ErrNoPrincipal = platformerrors.New("no principal on the passkeys service's request context")
)

// PrincipalIssuer mints a sign-in for a subject a credential has already
// proven. *signin.Service satisfies it, and it is the only door FinishLogin and
// AdminFinishLogin reach a token through.
//
// It is an interface so that a test can stand in for the issuer, and so that
// nothing else on signin.Service is reachable from here: this package holds
// the one method it is allowed to call and no other.
type PrincipalIssuer interface {
	IssueForPrincipal(
		ctx context.Context,
		scope tenancy.Scope,
		userID, activeAccountID string,
		opts ...signin.IssueOption,
	) (*signin.SignIn, error)
}

var _ PrincipalIssuer = (*signin.Service)(nil)

// Server is PasskeysService over passkeys.Service.
//
// # What it is
//
// Each RPC is one call into the service and a conversion on either side, with
// the one addition that is the reason this package exists: FinishLogin hands
// the login the service proved to a [PrincipalIssuer], so a passkey sign-in
// answers with the token a password sign-in answers with. [AnonymousMethods]
// and [SelfServiceMethods] are who may call what.
//
// # What it is not
//
// It holds no policy. Who may enroll is the service's EnrollmentGate, whether
// a named login exists is its UsernameResolver, whether the last passkey may
// go is its last-credential guard, and whether a passkey asserted on a key tap
// needs a second factor beside it is sign-in's. Who is calling is a
// [callers.Principal] the consumer's authentication interceptor put on the
// context, and whose directory a request is against is a [ScopeResolver].
//
// # Errors
//
// See the package documentation: the codes come from passkeys.GRPCMapper and
// signin.GRPCMapper, which a consumer registers through errormappers.Register,
// and this constructor deliberately registers neither.
type Server struct {
	passkeyspb.UnimplementedPasskeysServiceServer

	svc        *passkeys.Service
	client     database.Client
	issuer     PrincipalIssuer
	principals callers.PrincipalExtractor
	scopes     ScopeResolver
	handles    UserHandle

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	instruments *metrics.OperationSet
}

var _ passkeyspb.PasskeysServiceServer = (*Server)(nil)

// NewServer builds the gRPC surface over a passkey service.
//
// The database client is what the ceremonies run on: a begin and the list
// read on its reader, and an archive writes in a transaction this server
// opens, since the service's archive takes the caller's transaction and a
// gRPC request has none of its own. The issuer is what a finished login is minted
// through — *signin.Service, in every deployment but a test's.
//
// The scope resolver defaults to [GlobalScope] and the user handle to
// [UserIDHandle]; see each for when a deployment names its own.
func NewServer(
	svc *passkeys.Service,
	client database.Client,
	issuer PrincipalIssuer,
	principals callers.PrincipalExtractor,
	opts ...Option,
) (*Server, error) {
	switch {
	case svc == nil:
		return nil, ErrNilService
	case client == nil:
		return nil, ErrNilDatabaseClient
	case issuer == nil:
		return nil, ErrNilIssuer
	case principals == nil:
		return nil, ErrNilPrincipalExtractor
	}

	s := &Server{
		svc:        svc,
		client:     client,
		issuer:     issuer,
		principals: principals,
		scopes:     GlobalScope,
		handles:    UserIDHandle,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating passkeys grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server. Its signature is
// server/grpc's RegistrationFunc.
func (s *Server) RegisterOn(srv *grpc.Server) {
	passkeyspb.RegisterPasskeysServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, whose directory this is, and — for the self-service
// ones — who is calling.
type request struct {
	op        observability.Operation
	principal callers.Principal
	scope     tenancy.Scope
}

// anonymous is where the login RPCs start: the span, the instruments and the
// scope. The returned func is deferred by the caller with the error the RPC
// returns.
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
		// what it opened itself.
		done(err)

		return ctx, nil, func(error) {}, err
	}

	op.Set(scopeKey, scope.String())

	return ctx, &request{op: op, scope: scope}, done, nil
}

// caller is anonymous plus the principal the self-service RPCs need. The scope
// still comes off the resolver, so one wiring decision governs the service —
// see [WithScopeResolver].
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

// handle resolves the caller's WebAuthn user handle.
func (s *Server) handle(ctx context.Context, req *request) ([]byte, error) {
	handle, err := s.handles(ctx, req.scope, req.principal.UserID())
	if err != nil {
		return nil, platformerrors.Wrap(err, "resolving the caller's webauthn user handle")
	}

	return handle, nil
}

// operationAttr labels an instrument with the RPC it was recorded in.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("passkeys.rpc", method))
}

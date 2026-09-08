package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/database"
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
)

// serverName scopes this surface's spans, logger and instruments.
const serverName = "oauth2clients_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey  = "oauth2clients.scope"
	userIDKey = "oauth2clients.user_id"
	clientKey = "oauth2clients.id"
)

// The wiring failures this surface refuses to be built with.
var (
	// ErrNilService is a server built over no service.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients service")

	// ErrNilStore is a server built over no store. It is separate from the
	// service because the reads go straight to the store: a read is one call
	// with no hook and no transaction to own, so routing it through the service
	// would be a second name for the same query.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients store")

	// ErrNilDatabaseClient is a server built with no handle to read on.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients database client")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal hands the registry to anybody who can reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 clients principal extractor")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the oauth2 client registry's request context")

	// ErrNoPrincipalUser is a request whose caller names no person.
	//
	// It is separate from ErrNoPrincipal because it is a different failure: the
	// extractor returned somebody, and that somebody has no identifier. Only the
	// self-service half refuses it, and [Server.owner] is where — see there for
	// why an empty owner is a value on this surface rather than a missing one.
	ErrNoPrincipalUser = platformerrors.New("the caller of the oauth2 client registry names no user")
)

var _ oauth2clientspb.OAuth2ClientsServiceServer = (*Server)(nil)

// Server is the gRPC surface over an administered client registry.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, and what each method requires is a
// default map a consumer composes into their own — see [Permissions] and
// [Require].
type Server struct {
	oauth2clientspb.UnimplementedOAuth2ClientsServiceServer

	svc        *oauth2clients.Service
	store      oauth2clients.Store
	client     database.Client
	principals PrincipalExtractor
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a registry.
//
// It takes both the service and the store, as identity/grpc does and for the
// same reason: the writes are operations that own a transaction and run a
// consumer's hook inside it, and the reads are one query each with neither. A
// service method over a read would be a second name for the store's.
//
// The database client is what the reads run on — Reader(), outside any
// transaction — because a read on this surface has nothing to join.
//
// The principal extractor is positional; see [ErrNilPrincipalExtractor].
func NewServer(
	svc *oauth2clients.Service,
	store oauth2clients.Store,
	client database.Client,
	principals PrincipalExtractor,
	opts ...Option,
) (*Server, error) {
	if svc == nil {
		return nil, ErrNilService
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	s := &Server{svc: svc, store: store, client: client, principals: principals}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating oauth2clients grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting the registry
// beside the directory is one more entry in the slice that constructor already
// takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, clientsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	oauth2clientspb.RegisterOAuth2ClientsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, who is calling, and which registry that puts the
// request in.
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
// that read no principal answers with the global registry. The returned func is
// deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
//
// The scope comes off the principal and never off a request field. There is no
// ScopeResolver here, unlike authentication/signin/grpc: a caller reaching this
// surface has already become a principal, and a registry a client could name
// would be a cross-tenant read hiding behind a request field.
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

	return ctx, &request{op: op, principal: principal, scope: scope}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("oauth2clients.rpc", method))
}

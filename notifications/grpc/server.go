package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"

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
const serverName = "notifications_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey          = "notifications.scope"
	principalKey      = "notifications.principal"
	notificationIDKey = "notifications.notification_id"
	deviceIDKey       = "notifications.device_id"
	platformKey       = "notifications.platform"
	countKey          = "notifications.count"
)

// The wiring failures this surface refuses to be built with, and the two
// refusals a request meets before anything is read.
var (
	// ErrNilInbox is a server built over no inbox.
	//
	// It is separate from the registry because notifications declares the two
	// seams separately, and one value satisfies both: a consumer passes their
	// notifications.SQLStore twice. Both are required — a server that tolerated
	// a nil registry would answer three of its nine methods differently
	// depending on wiring a client cannot see.
	ErrNilInbox = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications inbox for the gRPC server")

	// ErrNilRegistry is a server built over no device registry.
	ErrNilRegistry = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications device registry for the gRPC server")

	// ErrNilDatabaseClient is a server built with no handle to read on and no way
	// to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the notifications gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal hands somebody's inbox to anybody who can reach the
	// port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil notifications principal extractor")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the notifications request context")

	// ErrNoPrincipalIdentifier is a caller the extractor resolved who has no
	// user identifier.
	//
	// It has no counterpart on the OAuth2 client registry next door, where such
	// a caller is an ordinary machine acting on administered rows. Here the
	// caller is the row's address: every statement behind these RPCs binds the
	// principal, and one that is empty names no inbox and no handset. The store
	// would refuse it as notifications.ErrEmptyPrincipal, which is a true
	// statement about an argument and a misleading one about a request — there
	// is no field for a client to correct — so the refusal is made here, as
	// codes.Unauthenticated.
	ErrNoPrincipalIdentifier = platformerrors.New("the notifications caller has no user identifier and so has no inbox")

	// ErrNilRegistrationInput is a registration that named no device at all.
	//
	// It is this package's own rather than notifications.ErrNilDevice, because
	// the two are different facts: that one is a nil argument inside the
	// process, which is a bug, and this one is a request whose input field was
	// never set, which is a client to correct.
	ErrNilRegistrationInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "device registration names no device")
)

var _ notificationspb.NotificationsServiceServer = (*Server)(nil)

// Server is the gRPC surface over an in-app inbox and a device registry.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, and what each method requires is a
// default map a consumer composes into their own — see [Permissions] and
// [Require].
type Server struct {
	notificationspb.UnimplementedNotificationsServiceServer

	inbox      notifications.Inbox
	registry   notifications.Registry
	client     database.Client
	principals PrincipalExtractor
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over an inbox and a device registry.
//
// The two seams are separate parameters because notifications declares them
// separately, and one value satisfies both: a consumer holding a
// notifications.SQLStore passes it twice. Neither may be nil — see [ErrNilInbox].
//
// The database client is two things here rather than one. It is what the reads
// run on, Reader(), outside any transaction, because a read on this surface has
// nothing to join. And it is what opens the transaction the writes need: every
// write in notifications takes a database.Tx, which only
// Client.WithTransaction produces, and an RPC handler is exactly the caller
// that convention describes as having nothing of its own to join.
//
// The principal extractor is positional; see [ErrNilPrincipalExtractor].
func NewServer(
	inbox notifications.Inbox,
	registry notifications.Registry,
	client database.Client,
	principals PrincipalExtractor,
	opts ...Option,
) (*Server, error) {
	if inbox == nil {
		return nil, ErrNilInbox
	}

	if registry == nil {
		return nil, ErrNilRegistry
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if principals == nil {
		return nil, ErrNilPrincipalExtractor
	}

	s := &Server{inbox: inbox, registry: registry, client: client, principals: principals}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating notifications grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting notifications
// beside the directory is one more entry in the slice that constructor already
// takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, notificationsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	notificationspb.RegisterNotificationsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, which directory the request is against, and whose
// inbox it is.
//
// The principal is carried, unlike on the OAuth2 client registry next door,
// because it is not a diagnostic here: it is bound into every statement this
// surface runs, and it is the whole of the row-level authorization — see the
// package documentation.
type request struct {
	op        observability.Operation
	principal string
	scope     tenancy.Scope
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than several lines per method because those lines can
// be got wrong separately — an RPC that forgot to count an attempt leaves a
// latency histogram with no denominator, one that forgot to end the span leaks
// it, one that read no principal reads somebody else's inbox. The returned func
// is deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
//
// The scope comes off the principal and never off a request field, and so does
// the recipient. There is no ScopeResolver here, unlike
// authentication/signin/grpc: a caller reaching this surface has already become
// a principal.
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

	// Both refusals below return before the RPC has deferred done, so each
	// closes what it opened itself — otherwise every anonymous request is a span
	// never ended and a failure nobody counted.
	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		err := grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipal, op.Logger(), op.Span(),
			codes.Unauthenticated, "resolving the caller of %s", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	recipient := principal.UserID()
	if recipient == "" {
		err := grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipalIdentifier, op.Logger(), op.Span(),
			codes.Unauthenticated, "resolving the inbox of the caller of %s", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	scope := principal.Scope()

	op.Set(scopeKey, scope.String())
	op.Set(principalKey, recipient)

	return ctx, &request{op: op, scope: scope, principal: recipient}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("notifications.rpc", method))
}

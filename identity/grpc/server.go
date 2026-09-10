package grpc

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/identitypb"

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

// serverName scopes this package's spans, logger and instruments.
const serverName = "identity_grpc"

// The keys this package attaches to spans and log lines. They match the ones
// identity's own layers use, so a trace crosses the transport boundary without
// the same fact arriving under two names.
const (
	scopeKey        = "identity.scope"
	userIDKey       = "identity.user_id"
	accountIDKey    = "identity.account_id"
	invitationIDKey = "identity.invitation_id"
)

// The errors this package returns for its own failures, as opposed to the
// store's and the service's.
var (
	// ErrNilDatabaseClient indicates a nil database.Client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the identity gRPC server")

	// ErrNilService indicates a nil *identity.Service. Every write here goes
	// through it, so there is no server that can be built without one.
	ErrNilService = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity service")

	// ErrNilStore indicates a nil identity.Store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil identity store for the gRPC server")

	// ErrNilPrincipalExtractor indicates a nil PrincipalExtractor.
	//
	// It is refused at construction rather than defaulted, because the only
	// default available is one that resolves nobody — and a directory server
	// that cannot say who is calling has no scope to filter its reads on. The
	// failure that would produce is a server answering every request with the
	// zero scope, which is a real directory rather than an empty one.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor")

	// ErrNoPrincipal indicates a request that arrived with nobody on the
	// context.
	//
	// It is a sentinel of its own rather than a wrap of a platform one, because
	// there is no platform sentinel for "unauthenticated" — the module has never
	// needed one, since authenticating is the consumer's and sessions maps its
	// own absence directly. Every RPC here answers it with
	// codes.Unauthenticated at the call site, so it needs no mapper.
	ErrNoPrincipal = platformerrors.New("no principal on the request context")

	// ErrInvitationTTLExceedsMaximum indicates a server whose default invitation
	// lifetime is longer than the ceiling it holds clients to. It is refused at
	// construction, since the alternative is a server that issues, on a request
	// naming nothing, an invitation it would have refused the request for
	// naming.
	ErrInvitationTTLExceedsMaximum = platformerrors.New("default invitation lifetime exceeds the maximum")

	// ErrInvitationExpiryInPast indicates a request that named an expiry no
	// later than the server's clock. Answered with codes.InvalidArgument at the
	// call site: an invitation that has expired before it is sent is a link the
	// consumer's hook mails out already dead.
	ErrInvitationExpiryInPast = platformerrors.New("invitation expiry is not in the future")

	// ErrInvitationExpiryTooFar indicates a request that named an expiry beyond
	// the server's maximum invitation lifetime. Answered with
	// codes.InvalidArgument at the call site. See DefaultMaxInvitationTTL for
	// why a named expiry is bounded at all.
	ErrInvitationExpiryTooFar = platformerrors.New("invitation expiry is beyond the maximum lifetime")

	// ErrEmailAddressUnverified indicates a caller whose stored email address has
	// not been proven reachable, on the one read that is keyed by it. Answered
	// with codes.FailedPrecondition at the call site: the address is the
	// caller's to change through UpdateProfile, so until it is verified it is a
	// claim rather than a fact, and a read keyed by a claim is a read keyed by
	// whatever the caller chose to claim.
	ErrEmailAddressUnverified = platformerrors.New("the calling user's email address is not verified")
)

// Server is IdentityService over identity.Service and identity.Store.
//
// # What it is
//
// Twenty-eight RPCs. Fifteen are writes, and each is exactly one call into
// identity.Service — which means each is one transaction with the consumer's
// own Hooks running inside it. Thirteen are reads on identity.Store, on the
// client's reader, and twelve of them are one call; the thirteenth,
// ListInvitationsForEmailAddress, reads the caller's user row first to learn
// the address, which is the price of not taking one from the request. There is
// no orchestration here: a method on this type converts, calls one thing, and
// converts back. That is deliberate, and it is what makes this file reviewable
// — anything that had to happen in a transaction happened one layer down, where
// the transaction is.
//
// # What it is not
//
// It holds no policy and decides nothing about who may call it. What each RPC
// requires is [Permissions], a default fragment a consumer composes into its own
// authorization policy and enforces with authorization/grpc's interceptor before
// a method here runs. Who is calling is a [Principal] the consumer's own
// authentication interceptor put on the context. Neither is optional and neither
// is here.
//
// It ships no credential RPCs. Setting a password, enrolling a second factor and
// proving an email address are the sign-in service's, and identity never hashes
// anything — see the proto's own documentation for why a plaintext password on
// this wire would be the transport making that choice.
//
// # Errors
//
// A method here hands the store's or the service's error back with a default
// code and does not switch on sentinels. What a taken username means on the
// wire is decided once, by identity.GRPCMapper, and a switch here would be the
// second copy of that decision.
//
// That mapper reaches a client only once it is registered, which is
// errormappers.Register — one call, made by service.Register for a service built
// from a service.Config and by a hand-assembled service itself. This
// constructor deliberately does not make it: a mapper that installs itself by a
// component being constructed is the process-wide side effect the module's rule
// exists to prevent. Without it every sentinel below arrives as codes.Unknown.
type Server struct {
	identitypb.UnimplementedIdentityServiceServer

	client database.Client
	store  identity.Store

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	svc             *identity.Service
	principals      PrincipalExtractor
	mintToken       TokenMinter
	targets         TargetAuthorizer

	instruments *metrics.OperationSet

	invitationTTL    time.Duration
	maxInvitationTTL time.Duration
}

var _ identitypb.IdentityServiceServer = (*Server)(nil)

// NewServer builds the gRPC surface over an identity service and store.
//
// The parameters read in the order every gRPC surface in this module takes
// them: the domain dependencies first, then the database client, then the
// principal extractor, then whatever authorizer the surface takes positionally
// — none here, because this one defaults its [TargetAuthorizer] and takes the
// replacement as [WithTargetAuthorizer]. A consumer mounting two of these
// services writes the same shape twice rather than looking each one up.
//
// The store is taken alongside the service rather than read off it because the
// two answer different halves — the Service owns the operations that are more
// than one write, the Store owns the reads — and a server built on a consumer's
// own Store implementation gets both.
//
// The client is kept for its Reader: a read here runs outside any transaction,
// on the reader handle, which is the executor half of the module's store
// convention. The writes take none, because each is a Service call and the
// Service opens its own.
//
// The principal extractor is positional rather than an option for the reason
// [ErrNilPrincipalExtractor] gives.
func NewServer(
	svc *identity.Service,
	store identity.Store,
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

	s := &Server{
		svc:              svc,
		store:            store,
		client:           client,
		principals:       principals,
		mintToken:        defaultTokenMinter,
		invitationTTL:    DefaultInvitationTTL,
		maxInvitationTTL: DefaultMaxInvitationTTL,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	// The row check, defaulted rather than left nil. A nil TargetAuthorizer
	// would be a server whose request-named RPCs are gated by the method-level
	// permission alone, which is the state this seam exists to end — so the
	// absence of an option is answered with the closed rule and not with none.
	if s.targets == nil {
		// Built into a variable and only then assigned to the interface field:
		// a constructor returning a concrete pointer into an interface on its
		// error path hands back a non-nil interface holding a nil pointer.
		authorizer, authorizerErr := NewMembershipAuthorizer(client, store)
		if authorizerErr != nil {
			return nil, authorizerErr
		}

		s.targets = authorizer
	}

	if s.invitationTTL > s.maxInvitationTTL {
		return nil, platformerrors.Wrapf(ErrInvitationTTLExceedsMaximum,
			"default %s exceeds maximum %s", s.invitationTTL, s.maxInvitationTTL)
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating identity grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting the directory is
// one entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{srv.RegisterOn}
//
// It is not called Register because that name is taken by the RPC that
// registers a user, which is the more important of the two and was here first.
func (s *Server) RegisterOn(srv *grpc.Server) {
	identitypb.RegisterIdentityServiceServer(srv, s)
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than four lines per method because the four can be
// got wrong separately: an RPC that forgot the principal reads with the zero
// scope, one that forgot to count an attempt leaves a latency histogram with no
// denominator, one that forgot to end the span leaks it. The returned func is
// deferred by the caller and closes all of it, and it is called with the error
// the RPC is returning — which is why every early return below assigns err
// before returning it, rather than returning a fresh value the deferred call
// never sees. When this helper itself fails it has already closed everything,
// and the func it hands back does nothing.
func (s *Server) caller(ctx context.Context, method string) (
	context.Context, observability.Operation, Principal, func(err error), error,
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
		err := grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipal, op.Logger(), op.Span(), codes.Unauthenticated, "resolving the caller of %s", method)

		// The RPC returns before it has deferred done, so this failure has to
		// close what it opened itself: otherwise every unauthenticated call is
		// a span never ended, a latency with no sample and a failure nobody
		// counted, on the one path the helper exists to keep honest.
		done(err)

		return ctx, op, nil, func(error) {}, err
	}

	op.Set(scopeKey, principal.Scope().String()).Set(userIDKey, principal.UserID())

	return ctx, op, principal, done, nil
}

// scopeOf is the one place a scope is produced, and it comes off the principal.
// See Principal.Scope for why there is no other source.
//
// It has no nil branch on purpose. caller refuses a request with no principal
// before any handler reaches this, so a nil here is a handler that skipped
// caller — and the only thing a nil branch could return is tenancy.Global,
// which would turn that mistake into a read of the global directory. A panic is
// the louder and the correct answer.
func scopeOf(p Principal) tenancy.Scope {
	return p.Scope()
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("identity.rpc", method))
}

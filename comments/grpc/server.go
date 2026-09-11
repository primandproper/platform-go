package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"

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

// serverName scopes this surface's spans, logger and instruments.
const serverName = "comments_grpc"

// The observability keys this surface attaches to its operations. They are the
// store's own keys, spelled the same way, so a span from a handler and a span
// from the statement beneath it name the same fact identically.
//
// The caller has a key of its own rather than sharing the author's. They are
// the same person on a create and are not on a moderator's edit, and a span
// that recorded one under the other would answer "who did this" with the name
// of whoever was being moderated.
const (
	scopeKey      = "comments.scope"
	callerKey     = "comments.caller"
	commentIDKey  = "comments.comment_id"
	parentIDKey   = "comments.parent_id"
	authorKey     = comments.AuthorAttributeKey
	targetTypeKey = "comments.target_type"
	targetIDKey   = "comments.target_id"
)

// The wiring failures this surface refuses to be built with, and the one
// refusal that is a request rather than a build.
var (
	// ErrNilStore is a server built over no store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil comments store for the gRPC server")

	// ErrNilDatabaseClient is a server built with no handle to read on and no
	// way to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil database client for the comments gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal attributes every comment to the same person and hands
	// every tenant's discussions to anybody who can reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"nil comments principal extractor")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the comments request context")

	// ErrNilCommentInput is a create that named no comment at all.
	//
	// It is this package's own rather than comments.ErrNilComment, because the
	// two are different facts: that one is a nil argument inside the process,
	// which is a bug, and this one is a request whose comment field was never
	// set, which is a client to correct.
	ErrNilCommentInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter,
		"comment creation names no comment")
)

var _ commentspb.CommentsServiceServer = (*Server)(nil)

// Server is the gRPC surface over a discussion.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, what each method requires is a default
// map a consumer composes into their own ([Permissions], declared in one call
// by [Require]), and whose comments a caller may reach is [AuthorAuthorizer],
// which unlike the other two has a default.
type Server struct {
	commentspb.UnimplementedCommentsServiceServer

	store      comments.Store
	client     database.Client
	principals PrincipalExtractor
	authors    AuthorAuthorizer
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a comment store.
//
// It takes the [comments.Store] seam rather than *comments.SQLStore, because
// nothing here is SQL: every method is one call into the interface, and a
// consumer who has implemented the store over something else gets this surface
// for free.
//
// The database client is two things here rather than one. It is what the reads
// run on, Reader(), outside any transaction, because a read on this surface has
// nothing to join. And it is what opens the transaction the writes need:
// comments.Store's writes take a database.Tx, which only Client.WithTransaction
// produces, so an RPC handler — a caller with genuinely nothing of its own to
// join — is exactly the caller that interface's documentation describes.
//
// The principal extractor is positional; see [ErrNilPrincipalExtractor].
func NewServer(
	store comments.Store,
	client database.Client,
	principals PrincipalExtractor,
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

	s := &Server{
		store:      store,
		client:     client,
		principals: principals,
		authors:    OwnCommentsOnly{},
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating comments grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting comments beside
// the directory is one more entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, commentsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	commentspb.RegisterCommentsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, whose discussion the request is against, and who is
// asking.
//
// The caller is carried whole as well as by identifier, because
// [AuthorAuthorizer] is handed the [Principal] rather than a string — a
// consumer's rule about whose words somebody may touch is written against
// whatever their own principal carries, and a surface that flattened it to a
// user id would decide that for them.
type request struct {
	op        observability.Operation
	principal Principal
	userID    string
	scope     tenancy.Scope
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than five lines per method because the five can be
// got wrong separately — an RPC that forgot to count an attempt leaves a
// latency histogram with no denominator, one that forgot to end the span leaks
// it, one that read no principal answers about somebody else's discussion. The
// returned func is deferred by the caller and is called with the error the RPC
// is returning, which is why every early return below assigns err before
// returning it.
//
// The scope comes off the principal and never off a request field. There is no
// ScopeResolver here, unlike authentication/signin/grpc: a caller reaching this
// surface has already become a principal, and a tenant a client could name
// would be a cross-tenant read hiding behind a request field — which on this
// surface means reading what another tenant's users have been saying to each
// other.
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
	op.Set(callerKey, principal.UserID())

	return ctx, &request{
		op:        op,
		principal: principal,
		userID:    principal.UserID(),
		scope:     scope,
	}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("comments.rpc", method))
}

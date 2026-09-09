package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

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
const serverName = "waitlists_grpc"

// The observability keys this surface attaches to its operations. They match the
// ones waitlists itself uses, so a trace crossing the transport boundary does
// not carry the same fact under two names.
const (
	scopeKey     = "waitlists.scope"
	userIDKey    = "waitlists.user_id"
	listKey      = "waitlists.list"
	signupKey    = "waitlists.signup"
	subjectKey   = "waitlists.subject_type"
	subjectIDKey = "waitlists.subject_id"
	countKey     = "waitlists.count"
)

// The wiring failures this surface refuses to be built with, and the one refusal
// that is a request rather than a wiring failure.
var (
	// ErrNilStore is a server built over no store.
	//
	// There is one dependency here where identity/grpc and webhooks/grpc each
	// have two, and the absence is the package rather than an omission:
	// waitlists ships a Store and no service, because nothing it does needs a
	// layer above the statements. The transitions that would live in one are
	// already single guarded updates.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlists store")

	// ErrNilDatabaseClient is a server built with no handle to read on and no
	// way to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the waitlists gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every administrative RPC answer Unauthenticated while the
	// signup page keeps working, which is a server that looks half alive, and
	// one returning a fabricated principal hands every tenant's signup list to
	// anybody who can reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlists principal extractor")

	// ErrNilSignupAuthorizer is a server built with no answer to "may this
	// caller withdraw this signup". See [SignupAuthorizer] for why there is no
	// default and why it is positional.
	ErrNilSignupAuthorizer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlists signup authorizer")

	// ErrNoPrincipal is a request to one of the administrative RPCs that arrived
	// with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there. Unlike every other surface in this module, that is
	// not by itself an error here — the three public RPCs answer such a request
	// — so this sentinel names the fourteen that do not.
	ErrNoPrincipal = platformerrors.New("no principal on the waitlists request context")

	// ErrNilListInput is a create or update that named no list at all.
	//
	// It is this package's own rather than waitlists.ErrNilList, because the two
	// are different facts: that one is a nil argument inside the process, which
	// is a bug, and this one is a request whose list field was never set, which
	// is a client to correct.
	ErrNilListInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "waitlist write names no list")

	// ErrNilSubject is a request whose subject message was never set.
	//
	// It is distinct from waitlists' ErrEmptySubjectType and ErrEmptySubjectID,
	// which are half a subject. This one is none of it, and on
	// WithdrawSignupsForSubject the difference matters: a subject bound to
	// nobody names every signup nobody claimed.
	ErrNilSubject = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "waitlist request names no subject")
)

var _ waitlistspb.WaitlistsServiceServer = (*Server)(nil)

// Server is the gRPC surface over a waitlist store.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, what each method requires is a default
// map a consumer composes into their own — see [Permissions] and [Require] — and
// whether the person clicking unsubscribe is the person who signed up is a seam
// the consumer answers, see [SignupAuthorizer].
type Server struct {
	waitlistspb.UnimplementedWaitlistsServiceServer

	store      waitlists.Store
	client     database.Client
	principals PrincipalExtractor
	signups    SignupAuthorizer
	scopes     ScopeResolver
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a waitlist store.
//
// It takes the store and no service, which is the shortest of the differences
// between this surface and the three before it: waitlists ships no service to
// take. Every write here is one statement, and the transaction it belongs in is
// opened by the handler with Client.WithTransaction — waitlists.Store's writes
// take a database.Tx, and an RPC handler is exactly the caller with nothing of
// its own to join that the Store's documentation describes.
//
// The database client is therefore two things: what the reads run on, Reader(),
// outside any transaction, and what opens the transaction the writes need.
//
// The principal extractor and the signup authorizer are positional; see
// [ErrNilPrincipalExtractor] and [SignupAuthorizer]. Neither has a default and
// neither could.
//
// The scope resolver is an option and defaults to [GlobalScope], which is the
// single-tenant answer and is asked only of requests that arrive with nobody on
// them.
func NewServer(
	store waitlists.Store,
	client database.Client,
	principals PrincipalExtractor,
	signups SignupAuthorizer,
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

	if signups == nil {
		return nil, ErrNilSignupAuthorizer
	}

	s := &Server{
		store:      store,
		client:     client,
		principals: principals,
		signups:    signups,
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
		return nil, platformerrors.Wrap(err, "creating waitlists grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting waitlists beside
// the directory is one more entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, waitlistsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	waitlistspb.RegisterWaitlistsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the operation
// to record on, whose catalog the request is against, and who is asking.
//
// The principal is carried and is nil on the three public RPCs, which is what
// makes this service's request struct different from the ones next door. Join
// reads it for the signup's subject, and [SignupAuthorizer] is handed it so that
// a consumer whose unsubscribe page is behind a sign-in can answer from the
// caller rather than from a token.
type request struct {
	op        observability.Operation
	principal Principal
	scope     tenancy.Scope
}

// visitor starts a public RPC: one a person on a signup page reaches, with or
// without having signed in.
//
// It is one helper rather than five lines per method because the five can be got
// wrong separately — an RPC that forgot to count an attempt leaves a latency
// histogram with no denominator, one that forgot to end the span leaks it, one
// that resolved no scope answers with the global catalog. The returned func is
// deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
//
// The scope is the principal's where there is a principal and the resolver's
// where there is not, and never a request field: a tenant a client could name is
// a cross-tenant write hiding behind one, which on this surface means joining
// somebody to another tenant's list or reading whose signups are on it.
func (s *Server) visitor(ctx context.Context, method string) (
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

	req := &request{op: op}

	principal, ok := s.principals(ctx)
	if ok && principal != nil {
		req.principal = principal
		req.scope = principal.Scope()

		op.Set(userIDKey, principal.UserID())
	} else {
		scope, err := s.scopes(ctx)
		if err != nil {
			err = grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(),
				codes.InvalidArgument, "resolving the catalog %s is against", method)

			// The RPC returns before it has deferred done, so this failure
			// closes what it opened itself — otherwise every unplaceable request
			// is a span never ended and a failure nobody counted.
			done(err)

			return ctx, nil, func(error) {}, err
		}

		req.scope = scope
	}

	op.Set(scopeKey, req.scope.String())

	return ctx, req, done, nil
}

// caller starts an administrative RPC: one that requires somebody to be calling.
//
// It is [Server.visitor] plus the refusal, because the fourteen it fronts differ
// from the three only in that. The permission interceptor in front of them has
// already refused an anonymous caller — a grant is a fact about somebody, and
// authorization/grpc is fail-closed — so this is the second lock rather than the
// first, and it is here because a consumer who declares this service's methods
// themselves can get the first one wrong.
func (s *Server) caller(ctx context.Context, method string) (
	context.Context, *request, func(err error), error,
) {
	ctx, req, done, err := s.visitor(ctx, method)
	if err != nil {
		return ctx, nil, done, err
	}

	if req.principal == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNoPrincipal, req.op.Logger(), req.op.Span(),
			codes.Unauthenticated, "resolving the caller of %s", method)

		done(err)

		return ctx, nil, func(error) {}, err
	}

	return ctx, req, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("waitlists.rpc", method))
}

package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

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
const serverName = "settings_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey        = "settings.scope"
	userIDKey       = "settings.user_id"
	definitionKey   = "settings.definition"
	definitionIDKey = "settings.definition_id"
	subjectTypeKey  = "settings.subject_type"
	subjectIDKey    = "settings.subject_id"
	countKey        = "settings.count"
)

// The wiring failures this surface refuses to be built with, and the two
// refusals a malformed request earns.
var (
	// ErrNilStore is a server built over no store.
	//
	// It is the only component this surface has: settings ships no service
	// layer, because every one of its writes is one statement plus the reads
	// that check it, all of them inside a transaction the caller owns. What
	// would be a service method here is this handler, and the transaction is the
	// handler's.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings store for the gRPC server")

	// ErrNilDatabaseClient is a server built with no handle to read on and no
	// way to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the settings gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal hands every tenant's catalog to anybody who can reach
	// the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings principal extractor")

	// ErrNilSubjectAuthorizer is a server built with no rule about whose
	// settings a caller may reach. See [SubjectAuthorizer] for why there is no
	// default.
	ErrNilSubjectAuthorizer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil settings subject authorizer")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the settings request context")

	// ErrNilDefinitionInput is a create or an update that named no definition at
	// all.
	//
	// It is this package's own rather than settings.ErrNilDefinition, because
	// the two are different facts: that one is a nil argument inside the
	// process, which is a bug, and this one is a request whose definition field
	// was never set, which is a client to correct.
	ErrNilDefinitionInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "settings write names no definition")

	// ErrNoValueNamed is a SetValue whose typed value named no case at all.
	//
	// It is not the same as a string_value of "", which is a value somebody
	// chose and which settings stores: the oneof is what keeps those two apart,
	// and this is the refusal for the half of the difference that is not a
	// value.
	ErrNoValueNamed = platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "settings write names no value")
)

var _ settingspb.SettingsServiceServer = (*Server)(nil)

// Server is the gRPC surface over a settings catalog and the answers stored
// against it.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, what each method requires is a default
// map a consumer composes into their own, and whose settings a caller may reach
// is a rule they supply — see [Permissions], [Require] and [SubjectAuthorizer].
type Server struct {
	settingspb.UnimplementedSettingsServiceServer

	store      settings.Store
	client     database.Client
	principals PrincipalExtractor
	subjects   SubjectAuthorizer
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a settings store.
//
// It takes one component rather than two, unlike identity/grpc and
// webhooks/grpc: settings has no service layer to take, because there is
// nothing above its store that a transaction does not already express. Each of
// these RPCs is one store call, or one store call and the read-back that
// follows it, and the transaction around a write is opened here.
//
// The database client is two things. It is what the reads run on, Reader(),
// outside any transaction, because a read on this surface has nothing to join.
// And it is what opens the transaction the writes need: settings.Store's writes
// take a database.Tx, which only Client.WithTransaction produces, so an RPC
// handler — a caller with genuinely nothing of its own to join — is exactly the
// caller that method's documentation describes.
//
// The principal extractor and the subject authorizer are positional; see
// [ErrNilPrincipalExtractor] and [SubjectAuthorizer].
func NewServer(
	store settings.Store,
	client database.Client,
	principals PrincipalExtractor,
	subjects SubjectAuthorizer,
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

	if subjects == nil {
		return nil, ErrNilSubjectAuthorizer
	}

	s := &Server{store: store, client: client, principals: principals, subjects: subjects}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating settings grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting settings beside
// the directory is one more entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, settingsSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	settingspb.RegisterSettingsServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, whose catalog the request is against, and who is
// asking.
//
// The caller is carried whole rather than as an identifier, because the six
// value RPCs hand it to the [SubjectAuthorizer] — the rule about whose settings
// this caller may reach is the consumer's, and it is given the principal they
// built rather than a field of it this package chose.
type request struct {
	op     observability.Operation
	caller Principal
	scope  tenancy.Scope
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than five lines per method because the five can be
// got wrong separately — an RPC that forgot to count an attempt leaves a
// latency histogram with no denominator, one that forgot to end the span leaks
// it, one that read no principal answers about another tenant's catalog. The
// returned func is deferred by the caller and is called with the error the RPC
// is returning, which is why every early return below assigns err before
// returning it.
//
// The scope comes off the principal and never off a request field. There is no
// ScopeResolver here, unlike authentication/signin/grpc: a caller reaching this
// surface has already become a principal, and a scope a client could name would
// be a cross-tenant read hiding behind a request field — on this surface, a
// read or a write of another tenant's catalog.
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

	return ctx, &request{op: op, scope: scope, caller: principal}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("settings.rpc", method))
}

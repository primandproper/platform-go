package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"

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
const serverName = "billing_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey        = "billing.scope"
	userIDKey       = "billing.user_id"
	accountKey      = "billing.account_id"
	productKey      = "billing.product_id"
	subscriptionKey = "billing.subscription_id"
	purchaseKey     = "billing.purchase_id"
	transactionKey  = "billing.transaction_id"
)

// The wiring failures this surface refuses to be built with.
var (
	// ErrNilStore is a server built over no store.
	//
	// There is no service beside it, unlike identity/grpc and
	// authentication/oauth2clients/grpc: billing ships a Store and nothing over
	// it, because every judgement a service layer would hold here — what a
	// status means, when to charge — is deliberately the consumer's. So the
	// writes on this surface open their own transaction through
	// Client.WithTransaction, which is the one way in.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil store for the billing grpc surface")

	// ErrNilDatabaseClient is a server built with no handle to read on and no
	// way to open the transaction its writes run in.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the billing grpc surface")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal hands the ledger to anybody who can reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor for the billing grpc surface")

	// ErrNilAccountAuthorizer is a server built with no rule about which
	// accounts a caller has standing in. See [AccountAuthorizer] for why this
	// one has no default either, where identity/grpc's equivalent does.
	ErrNilAccountAuthorizer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil account authorizer for the billing grpc surface")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the billing request context")

	// ErrNilInput is a write whose request carried no input message. It is
	// refused as malformed rather than passed through as an empty entity, which
	// the store would refuse with a message about a field the caller never sent.
	ErrNilInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil input on a billing grpc request")
)

var _ billingpb.BillingServiceServer = (*Server)(nil)

// Server is the gRPC surface over what a deployment sells and what its
// customers paid.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, what each method requires is a default
// map a consumer composes into their own, and which accounts a caller has
// standing in is a seam — see [Permissions], [Require] and [AccountAuthorizer].
//
// What it also does not ship is a reading of any status. There is no RPC here
// that says whether an account is entitled, and no field on any message that
// says so either; the surface hands back Subscription.Status and stops. See this
// package's documentation.
type Server struct {
	billingpb.UnimplementedBillingServiceServer

	store      billing.Store
	client     database.Client
	principals PrincipalExtractor
	targets    AccountAuthorizer
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a billing store.
//
// It takes the store rather than a service because billing ships no service:
// there is no layer here holding a judgement about what a status means, and one
// would have had to. The writes on this surface therefore open their own
// transaction through the database client's WithTransaction, which is also what
// the reads take their Reader() from.
//
// The principal extractor and the account authorizer are both positional and
// both required; see [ErrNilPrincipalExtractor] and [AccountAuthorizer] for why
// neither has a default.
func NewServer(
	store billing.Store,
	client database.Client,
	principals PrincipalExtractor,
	targets AccountAuthorizer,
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

	if targets == nil {
		return nil, ErrNilAccountAuthorizer
	}

	s := &Server{store: store, client: client, principals: principals, targets: targets}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating billing grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting billing beside
// the directory is one more entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, billingSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	billingpb.RegisterBillingServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, the scope the caller's principal puts the request in,
// and the caller themselves.
//
// The principal is carried, unlike on authentication/oauth2clients/grpc, because
// six of these RPCs ask [AccountAuthorizer] about it after the request has been
// read.
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
// that read no principal answers with somebody else's catalog. The returned func
// is deferred by the caller and is called with the error the RPC is returning,
// which is why every early return below assigns err before returning it.
//
// The scope comes off the principal and never off a request field. No message in
// billing.proto has one, and that is the load-bearing absence: every store read
// filters on the scope it is handed, so handing it one the caller chose would
// make the filter answer to the caller.
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

// requireAccount refuses a request that named no account, before the authorizer
// is asked about it.
//
// It is here rather than left to the store or to the seam because an empty
// account is malformed, and malformed is answered as malformed whoever sent it.
// The store would refuse it too — billing.ErrEmptyAccount exists precisely so a
// page of one customer's invoices cannot degrade into a page of everybody's —
// and an AccountAuthorizer that happened to permit the empty string would
// otherwise have reached it.
func (s *Server) requireAccount(
	req *request,
	accountID string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	if accountID != "" {
		return nil
	}

	return grpcerrors.PrepareAndLogGRPCStatus(billing.ErrEmptyAccount, req.op.Logger(), req.op.Span(),
		codes.InvalidArgument, descriptionFmt, descriptionArgs...)
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("billing.rpc", method))
}

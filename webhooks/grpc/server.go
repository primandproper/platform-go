package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

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
const serverName = "webhooks_grpc"

// The observability keys this surface attaches to its operations.
const (
	scopeKey             = "webhooks.scope"
	userIDKey            = "webhooks.user_id"
	endpointKey          = "webhooks.endpoint_id"
	subscriptionKey      = "webhooks.subscription_id"
	deliveryKey          = "webhooks.delivery_id"
	eventTypeKey         = "webhooks.event_type"
	endpointURLKey       = "webhooks.endpoint_url"
	subscriptionCountKey = "webhooks.subscription_count"
)

// The wiring failures this surface refuses to be built with.
var (
	// ErrNilDispatcher is a server built over no dispatcher.
	//
	// The dispatcher rather than the store is what the three gated writes go
	// through, because the gates are its: Register validates the URL an
	// authenticated request is about to be made to, and Subscribe checks the
	// event type against the consumer's catalog. A surface that wrote through the
	// store would be a surface that accepted an endpoint pointed at
	// 169.254.169.254 and a subscription to an event nothing publishes.
	ErrNilDispatcher = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhooks dispatcher")

	// ErrNilStore is a server built over no store. It is separate from the
	// dispatcher because the reads go straight to it: a read is one query with
	// no catalog to check and no transaction to own, and the dispatcher offers
	// none of them.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhooks store for the gRPC server")

	// ErrNilDatabaseClient is a server built with no handle to read on and no way
	// to open the transaction its writes need.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the webhooks gRPC server")

	// ErrNilPrincipalExtractor is a server built with no way to tell who is
	// calling.
	//
	// It is positional rather than an option, and there is no default, because
	// every default is wrong in a way nothing reports: one returning no
	// principal makes every RPC answer Unauthenticated, and one returning a
	// fabricated principal hands every tenant's endpoints to anybody who can
	// reach the port.
	ErrNilPrincipalExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhooks principal extractor")

	// ErrNoPrincipal is a request that arrived with no caller on its context.
	//
	// It is a refusal rather than a wiring failure: the extractor worked and
	// there was nobody there, which is what an unauthenticated request looks
	// like once a consumer's interceptor has run.
	ErrNoPrincipal = platformerrors.New("no principal on the webhooks request context")

	// ErrNilEndpointInput is a save that named no endpoint at all.
	//
	// It is this package's own rather than webhooks.ErrNilEndpoint, because the
	// two are different facts: that one is a nil argument inside the process,
	// which is a bug, and this one is a request whose endpoint field was never
	// set, which is a client to correct.
	ErrNilEndpointInput = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "webhook endpoint save names no endpoint")
)

var _ webhookspb.WebhooksServiceServer = (*Server)(nil)

// Server is the gRPC surface over webhook endpoint management.
//
// It ships the transport and not the policy, which is the same bargain
// identity/grpc states: who is calling is an interface a consumer's own
// authentication interceptor satisfies, and what each method requires is a
// default map a consumer composes into their own — see [Permissions] and
// [Require].
type Server struct {
	webhookspb.UnimplementedWebhooksServiceServer

	dispatcher webhooks.Dispatcher
	store      webhooks.Store
	client     database.Client
	principals PrincipalExtractor
	o11y       observability.Observer

	instruments *metrics.OperationSet

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewServer builds the gRPC surface over a dispatcher and the store beneath it.
//
// It takes both, as identity/grpc takes a service and a store and for the same
// reason: the writes are operations with a gate in front of them, and the reads
// are one query each with neither a gate nor a transaction. A dispatcher method
// over a read would be a second name for the store's — and there is none, which
// is the seam saying the same thing.
//
// The database client is two things here rather than one. It is what the reads
// run on, Reader(), outside any transaction, because a read on this surface has
// nothing to join. And it is what opens the transaction the writes need:
// webhooks.Dispatcher's methods take a database.Tx, which only
// Client.WithTransaction produces, so an RPC handler — a caller with genuinely
// nothing of its own to join — is exactly the caller that method's
// documentation describes.
//
// The principal extractor is positional; see [ErrNilPrincipalExtractor].
func NewServer(
	dispatcher webhooks.Dispatcher,
	store webhooks.Store,
	client database.Client,
	principals PrincipalExtractor,
	opts ...Option,
) (*Server, error) {
	if dispatcher == nil {
		return nil, ErrNilDispatcher
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

	s := &Server{dispatcher: dispatcher, store: store, client: client, principals: principals}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating webhooks grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server.
//
// Its signature is server/grpc's RegistrationFunc, so mounting webhooks beside
// the directory is one more entry in the slice that constructor already takes:
//
//	[]grpcserver.RegistrationFunc{identitySrv.RegisterOn, webhooksSrv.RegisterOn}
func (s *Server) RegisterOn(srv *grpc.Server) {
	webhookspb.RegisterWebhooksServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the
// operation to record on, whose endpoints the request is against, and who is
// asking.
//
// The caller's identifier is carried, unlike on the OAuth2 client registry next
// door, because one method has a use for it that is not a policy decision:
// SaveEndpoint writes it into Endpoint.CreatedBy, so an endpoint's provenance
// comes off the connection rather than out of a request field.
type request struct {
	op     observability.Operation
	userID string
	scope  tenancy.Scope
}

// caller resolves the principal, the scope and an operation, in the one place
// every RPC starts.
//
// It is one helper rather than five lines per method because the five can be got
// wrong separately — an RPC that forgot to count an attempt leaves a latency
// histogram with no denominator, one that forgot to end the span leaks it, one
// that read no principal answers about somebody else's endpoints. The returned
// func is deferred by the caller and is called with the error the RPC is
// returning, which is why every early return below assigns err before returning
// it.
//
// The scope comes off the principal and never off a request field. There is no
// ScopeResolver here, unlike authentication/signin/grpc: a caller reaching this
// surface has already become a principal, and a tenant a client could name would
// be a cross-tenant read hiding behind a request field — which on this surface
// means reading where another tenant's events are being sent.
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

	return ctx, &request{op: op, scope: scope, userID: principal.UserID()}, done, nil
}

// operationAttr labels an instrument with the RPC it was recorded in. It is the
// generated full method name, so a dashboard's series match what the
// authorization table and the access log call the same call.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("webhooks.rpc", method))
}

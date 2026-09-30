package grpc

import (
	"context"
	"errors"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	"github.com/primandproper/platform-go/v14/mediaregistry/mediaregistrypb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/uploads"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// serverName scopes this surface's spans, logger and instruments.
const serverName = "mediaregistry_grpc"

// The observability keys this surface attaches to its operations. The object is
// labeled with the registry's own exported key, so a span from here and one
// from the store beneath it name the same object the same way.
const (
	objectIDKey    = mediaregistry.ObjectAttributeKey
	principalIDKey = serverName + ".principal_id"
	scopeKey       = serverName + ".scope"
	objectKeyKey   = serverName + ".object_key"
	sizeKey        = serverName + ".size"

	// refusedKey records why a request was answered as an absence. The client
	// is told nothing; the operator reading the span is told which rule it was.
	refusedKey = serverName + ".refused"
)

// The wiring failures this surface refuses to be built with, and the requests
// it refuses as malformed. Every refusal of a request that is well formed is
// mediaregistry.ErrObjectNotFound instead; see the package documentation.
var (
	// ErrNilCallerResolver is a server built without a caller resolver.
	//
	// It has no default for the reason mediaregistry/http's has none: every
	// RPC here is about whose objects these are, and a default would have to
	// invent the answer.
	ErrNilCallerResolver = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil media registry gRPC caller resolver")

	// ErrNoPrincipal is a request whose caller resolved with no principal
	// identifier: nobody to own an upload, and nobody whose objects to list.
	ErrNoPrincipal = platformerrors.Wrap(callers.ErrNoPrincipal, "no principal on the media registry request")

	// ErrNoUploadHeader is an upload whose first message is not a header —
	// including one that sent nothing at all.
	ErrNoUploadHeader = platformerrors.New("an upload's first message must be its header")

	// ErrRepeatedUploadHeader is an upload that sent a second header after its
	// bytes had begun.
	ErrRepeatedUploadHeader = platformerrors.New("an upload carries exactly one header")

	// ErrInvalidObjectName is an upload whose name cannot be the last segment of
	// a key: empty, "." or "..", or carrying a path separator.
	ErrInvalidObjectName = platformerrors.New("an object name must be one non-empty path segment")

	// ErrContentTypeRefused is an object whose declared type the deployment's
	// ContentTypePolicy does not accept.
	ErrContentTypeRefused = platformerrors.New("objects of this content type are not accepted")

	// ErrObjectTooLarge is an upload that went past the deployment's cap. It is
	// raised the moment the cap is crossed, with the rest of the stream unread.
	ErrObjectTooLarge = platformerrors.New("the object is larger than this service accepts")

	// ErrNoObjectKey is a registration that named no key.
	ErrNoObjectKey = platformerrors.New("a registration must name the key its bytes are at")
)

var _ mediaregistrypb.MediaRegistryServiceServer = (*Server)(nil)

// Server is the gRPC surface over the media registry.
//
// It ships the transport and the defaults, and every policy it holds is a value
// a deployment replaces with an option: who is calling, who may read what, where
// in the bucket an upload goes, which types and how large, which keys a caller
// may claim, and what happens once an upload lands. The package documentation
// says what each defaults to and why.
type Server struct {
	mediaregistrypb.UnimplementedMediaRegistryServiceServer

	store   mediaregistry.Store
	client  database.Client
	manager uploads.UploadManager

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	resolver     mediaregistryhttp.CallerResolver
	entitlement  mediaregistryhttp.Entitlement
	keys         KeyFunc
	contentTypes ContentTypePolicy
	recordKeys   RecordKeyPolicy
	afterUpload  AfterUpload
	instruments  *metrics.OperationSet

	maxBytes int64
}

// NewServer builds the surface over a registry store, the client its reads run
// on and its writes open transactions with, and the manager that holds the
// bytes.
//
// The three are parameters because none of them can be defaulted. A
// CallerResolver is required as an option and has no default; see
// ErrNilCallerResolver. Everything else defaults, and the package
// documentation lists what to.
func NewServer(
	store mediaregistry.Store,
	client database.Client,
	manager uploads.UploadManager,
	opts ...Option,
) (*Server, error) {
	switch {
	case store == nil:
		return nil, mediaregistry.ErrNilStore
	case client == nil:
		return nil, mediaregistry.ErrNilDatabaseClient
	case manager == nil:
		return nil, mediaregistry.ErrNilUploadManager
	}

	s := &Server{
		store:        store,
		client:       client,
		manager:      manager,
		entitlement:  mediaregistryhttp.OwnerOnly,
		keys:         DefaultKeyFunc,
		contentTypes: activeOrUnstated,
		maxBytes:     DefaultMaxBytes,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}

	if s.resolver == nil {
		return nil, ErrNilCallerResolver
	}

	// Built over whichever layout the options settled on, so a deployment that
	// moved its keys moves the part of the bucket a caller may claim with them.
	if s.recordKeys == nil {
		s.recordKeys = KeysUnderPrefix(s.keys)
	}

	s.o11y = observability.NewObserver(serverName, s.logger, s.tracerProvider)

	instruments, err := metrics.NewOperationSet(s.metricsProvider, serverName)
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating mediaregistry grpc instruments")
	}

	s.instruments = instruments

	return s, nil
}

// RegisterOn mounts this service on a gRPC server. Its signature is
// server/grpc's RegistrationFunc.
func (s *Server) RegisterOn(srv *grpc.Server) {
	mediaregistrypb.RegisterMediaRegistryServiceServer(srv, s)
}

// request is what every RPC here resolves before it does anything: the operation
// to record on, and who is asking.
type request struct {
	op     observability.Operation
	caller mediaregistryhttp.Caller
}

// caller resolves the caller and an operation, in the one place every RPC
// starts. The returned func is deferred by the RPC and called with the error it
// is returning.
//
// A caller whose resolver answered with no principal identifier is refused as
// unauthenticated rather than served. Every RPC here is about whose objects
// these are, and "nobody's" is not an answer any of them can use.
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

	caller, err := s.resolver(ctx)
	if err == nil && caller.PrincipalID == "" {
		err = ErrNoPrincipal
	}

	if err == nil {
		err = caller.Scope.Validate()
	}

	if err != nil {
		code := codes.Internal
		if errors.Is(err, callers.ErrNoPrincipal) {
			code = codes.Unauthenticated
		}

		err = grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), code, "resolving the caller of %s", method)

		// The RPC returns before it has deferred done, so this failure closes
		// what it opened itself.
		done(err)

		return ctx, nil, func(error) {}, err
	}

	op.Set(principalIDKey, caller.PrincipalID)
	op.Set(scopeKey, caller.Scope.String())

	return ctx, &request{op: op, caller: caller}, done, nil
}

// absent is every refusal of a well-formed request: one error, one code, one
// message, whatever the reason. The reason is recorded on the span, which the
// client does not see.
//
// It is built from the sentinel itself rather than a wrapping of it, so the
// chain a peer decodes out of the status details is the same for every refusal
// too — an absence that carried "not entitled" in its details would be an
// oracle with one more step.
func (s *Server) absent(req *request, reason string) error {
	req.op.Set(refusedKey, reason)

	return grpcerrors.PrepareAndLogGRPCStatus(mediaregistry.ErrObjectNotFound,
		req.op.Logger(), req.op.Span(), codes.NotFound, "object not found")
}

// fail reports an error that is not a refusal, under defaultCode unless a
// registered mapper claims it.
func (s *Server) fail(req *request, err error, defaultCode codes.Code, format string, args ...any) error {
	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), defaultCode, format, args...)
}

// entitled reports whether the caller may read object, through the
// deployment's Entitlement. A nil row is not an entitlement: it is treated as
// an absence rather than handed to a rule that would dereference it.
func (s *Server) entitled(ctx context.Context, req *request, object *mediaregistry.Object) (bool, error) {
	if object == nil {
		return false, nil
	}

	return s.entitlement(ctx, req.caller, object)
}

// operationAttr labels an instrument with the RPC it was recorded in.
func operationAttr(method string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("mediaregistry.rpc", method))
}

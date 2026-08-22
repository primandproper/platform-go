package grpc

import (
	"context"
	stderrors "errors"
	"fmt"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	grpcerrors "github.com/primandproper/platform-go/v13/errors/grpc"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const serviceName = "idempotency_grpc"

// ErrNilManager indicates NewUnaryServerInterceptor was called without a
// manager.
var ErrNilManager = platformerrors.New("nil idempotency manager")

// interceptor holds what every call needs, built once at construction.
type interceptor struct {
	manager *idempotency.Manager[Response]
	cfg     *config
	o11y    observability.Observer

	unsupportedCounter metrics.Int64Counter
	truncatedCounter   metrics.Int64Counter
}

// NewUnaryServerInterceptor builds an interceptor that runs a handler at most
// once per idempotency key.
//
// Calls without the key in their incoming metadata pass through untouched, so
// only clients that opted in are affected. Register it the usual way — the
// platform's gRPC server already accepts a slice of unary interceptors and
// chains them.
//
// Streaming is out of scope. A stream has no single request to fingerprint and
// no single reply to record, so the same treatment would not mean anything.
func NewUnaryServerInterceptor(
	manager *idempotency.Manager[Response],
	opts ...Option,
) (grpc.UnaryServerInterceptor, error) {
	if manager == nil {
		return nil, ErrNilManager
	}

	cfg := newConfig(opts...)
	i := &interceptor{
		manager: manager,
		cfg:     cfg,
		o11y:    observability.NewObserver(serviceName, cfg.logger, cfg.tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(cfg.metricsProvider)

	var err error
	if i.unsupportedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_unsupported_calls", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating unsupported calls counter")
	}
	if i.truncatedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_replies_truncated", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating replies truncated counter")
	}

	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		_, ok := keyFromIncoming(ctx, cfg.metadataKey)
		if !ok || (cfg.methodFilter != nil && !cfg.methodFilter(info.FullMethod)) {
			return handler(ctx, req)
		}

		return i.serve(ctx, req, info, handler)
	}, nil
}

// serve handles one keyed call.
func (i *interceptor) serve(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	ctx, op := i.o11y.Begin(ctx)
	defer op.End()

	key, _ := keyFromIncoming(ctx, i.cfg.metadataKey)

	principal := ""
	if i.cfg.principal != nil {
		var err error
		if principal, err = i.cfg.principal(ctx); err != nil {
			return nil, op.Error(err, "extracting call principal")
		}
	}

	// grpc-go allows non-proto codecs. Such a call cannot be fingerprinted, and
	// refusing it would break a service that never asked for any of this, so it
	// runs unguarded and is counted.
	message, ok := req.(proto.Message)
	if !ok {
		i.unsupportedCounter.Add(ctx, 1)
		op.Acknowledge(ErrNotProtoMessage, "fingerprinting non-proto request")

		return handler(ctx, req)
	}

	fp, err := fingerprint(info.FullMethod, principal, message)
	if err != nil {
		return nil, op.Error(err, "fingerprinting request")
	}

	// The handler's own reply and error are kept so the executed path can
	// return them directly. Rebuilding from the record would cost a marshal
	// and an unmarshal on every first call, and would answer a truncated
	// record with a failure even though the caller is holding the real reply.
	var (
		rawReply any
		rawErr   error
	)

	result, err := i.manager.Do(ctx, key, fp, func(ctx context.Context) (*Response, error) {
		rawReply, rawErr = handler(ctx, req)

		return record(rawReply, rawErr, i.cfg.maxResponseBytes)
	})
	if err != nil {
		if stderrors.Is(err, ErrNotProtoMessage) {
			// The handler has already run inside Do, and its claim was
			// released when recording failed. Returning what it produced is
			// both correct and the only option that does not run it twice.
			i.unsupportedCounter.Add(ctx, 1)
			op.Acknowledge(err, "recording non-proto reply")

			return rawReply, rawErr
		}

		// errors/grpc maps the idempotency sentinels, so ErrInFlight becomes
		// Aborted and ErrFingerprintMismatch InvalidArgument without this
		// package restating either.
		return nil, op.GRPCStatus(err, grpcerrors.MapToGRPC(err, codes.Internal), "running idempotent handler")
	}

	if !result.Replayed {
		return rawReply, rawErr
	}

	if result.Value.Truncated {
		i.truncatedCounter.Add(ctx, 1)
	}

	reply, err := replay(result.Value)
	if err != nil {
		if _, isStatus := status.FromError(err); isStatus {
			return nil, err
		}

		return nil, op.GRPCStatus(err, codes.Internal, "rebuilding recorded reply")
	}

	return reply, nil
}

// keyFromIncoming reads the key from a call's incoming metadata. The conversion
// to idempotency.Key happens here, at the wire boundary, so nothing downstream
// handles the key as a bare string.
func keyFromIncoming(ctx context.Context, metadataKey string) (idempotency.Key, bool) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", false
	}

	values := md.Get(metadataKey)
	if len(values) == 0 || values[0] == "" {
		return "", false
	}

	return idempotency.Key(values[0]), true
}

package grpc

import (
	"context"
	"fmt"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
)

// serviceName names this component's logger, tracer, and metrics.
const serviceName = "ratelimiting_grpc"

// ErrNilLimiter indicates the interceptor was built without a limiter.
var ErrNilLimiter = platformerrors.New("nil rate limiter")

// limitedError is the single refusal value this package returns.
//
// It satisfies three requirements at once, the way authorization/grpc's denial
// does: status.FromError recognizes GRPCStatus, so the wire code is right even
// with no error-encoding interceptor installed; errors.Is matches
// ratelimiting.ErrRateLimited, so an in-process caller can branch on the same
// sentinel a client would be handed; and the client-visible message is a
// constant, so a refused caller learns nothing about the limit it hit.
type limitedError struct {
	retryAfter time.Duration
	hasHint    bool
}

func (limitedError) Error() string { return ratelimiting.ErrRateLimited.Error() }

func (limitedError) Unwrap() error { return ratelimiting.ErrRateLimited }

// GRPCStatus renders the refusal, carrying the retry hint as RetryInfo.
//
// RetryInfo is gRPC's Retry-After: it is the one field the canonical error
// model defines for "come back later", and generated clients and proxies
// already know to read it. A refusal without it leaves every client guessing,
// and clients that guess tend to guess the same interval — which turns shed
// load into a synchronized retry storm rather than removing it.
func (e limitedError) GRPCStatus() *status.Status {
	st := status.New(codes.ResourceExhausted, ratelimiting.ErrRateLimited.Error())
	if !e.hasHint {
		return st
	}

	// A detail that will not attach is not worth failing the refusal over: the
	// code is the part the client must have, and the hint is the improvement.
	if withDetails, err := st.WithDetails(&errdetails.RetryInfo{
		RetryDelay: durationpb.New(e.retryAfter),
	}); err == nil {
		return withDetails
	}

	return st
}

// guard holds what every RPC needs, built once at construction.
type guard struct {
	limiter ratelimiting.RateLimiter
	key     KeyFunc
	cfg     *config
	o11y    observability.Observer

	allowedCounter metrics.Int64Counter
	refusedCounter metrics.Int64Counter
	errorCounter   metrics.Int64Counter
}

// NewUnaryServerInterceptor builds a unary interceptor that spends a token per
// RPC and answers RESOURCE_EXHAUSTED when there is none.
//
// It is the gRPC half of what ratelimiting/http's middleware does over HTTP,
// over the same ratelimiting.RateLimiter — so a service serving both transports
// can hold one limiter and have a caller's budget mean the same thing on either.
//
// keyFn decides what the limit is per; see KeyByPeer and its neighbors. There is
// no default, because the wrong one is worse than none.
//
// Install it before the handler and after authentication, so that a keyFn
// reading a principal has one to read:
//
//	grpc.ChainUnaryInterceptor(authInterceptor, rateLimitInterceptor)
//
// Streams are not covered. A stream spends one token at open and then runs
// unbounded, which measures the wrong thing badly enough to be worth leaving
// out rather than shipping as a guard people trust.
func NewUnaryServerInterceptor(
	limiter ratelimiting.RateLimiter,
	keyFn KeyFunc,
	opts ...Option,
) (grpc.UnaryServerInterceptor, error) {
	if limiter == nil {
		return nil, ErrNilLimiter
	}

	if keyFn == nil {
		return nil, ErrNilKeyFunc
	}

	cfg := newConfig(opts...)

	g := &guard{
		limiter: limiter,
		key:     keyFn,
		cfg:     cfg,
		o11y:    observability.NewObserver(serviceName, cfg.logger, cfg.tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(cfg.metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&g.allowedCounter, "allowed"},
		{&g.refusedCounter, "refused"},
		{&g.errorCounter, "errors"},
	}
	for _, c := range counters {
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", serviceName, c.name))
		if err != nil {
			return nil, platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	return g.intercept, nil
}

// intercept runs the guard for one RPC and, if it passes, the handler.
//
// The decision owns the span and the handler runs outside it — deliberately. A
// span held open across the handler would make every trace read as though the
// rate limiter took the whole RPC, when what it did was one check.
func (g *guard) intercept(
	ctx context.Context,
	req any,
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	if err := g.admit(ctx, info); err != nil {
		return nil, err
	}

	return handler(ctx, req)
}

// admit spends a token for the RPC and returns the refusal to send back, or nil
// to let the handler run.
func (g *guard) admit(ctx context.Context, info *grpc.UnaryServerInfo) error {
	ctx, op := g.o11y.Begin(ctx)
	defer op.End()

	// The method is the one attribute these counters carry. It comes from the
	// service definition rather than from the caller, so it is bounded — unlike
	// the key, which identifies a caller and stays on the span.
	attrs := metric.WithAttributes(attribute.String(keys.RateLimitMethodKey, info.FullMethod))
	op.Set(keys.RateLimitMethodKey, info.FullMethod)

	key, err := g.key(ctx, info)
	if err != nil {
		return g.fail(ctx, op, attrs, err, "extracting the rate limit key")
	}

	if key == "" {
		// Exempted by the extractor. Not counted as allowed: nothing was spent.
		return nil
	}

	op.SpanOnly(keys.RateLimitKeyKey, key)

	allowed, err := g.limiter.Allow(ctx, key)
	if err != nil {
		return g.fail(ctx, op, attrs, err, "consulting the rate limiter")
	}

	if !allowed {
		g.refusedCounter.Add(ctx, 1, attrs)

		refusal := limitedError{}
		if delay, hinted := g.retryAfter(ctx, key); hinted {
			refusal = limitedError{retryAfter: delay, hasHint: true}
			op.SpanOnly(keys.RetryAfterKey, delay.String())
		}

		// Debug rather than error: a limiter refusing an RPC is the limiter
		// working, and under the traffic that triggers this, a line per refusal
		// is the log volume the refusal was meant to prevent.
		op.Logger().Debug("rate limited")

		return refusal
	}

	g.allowedCounter.Add(ctx, 1, attrs)

	return nil
}

// fail resolves an RPC the limiter could not rule on, per WithFailClosed,
// returning the refusal to send back or nil to let the handler run.
func (g *guard) fail(
	ctx context.Context,
	op observability.Operation,
	attrs metric.MeasurementOption,
	err error,
	description string,
) error {
	g.errorCounter.Add(ctx, 1, attrs)

	// Error, unlike a refusal: a guard that cannot answer is a fault, and it is
	// invisible in traffic whichever way it resolves.
	op.Acknowledge(err, "%s", description)

	if !g.cfg.failClosed {
		return nil
	}

	// No hint: nothing measured a wait, so there is no honest number to give.
	return limitedError{}
}

// retryAfter resolves the hint for a refusal: the limiter's own estimate when
// it has one, the configured fallback otherwise, and nothing when that fallback
// has been suppressed.
func (g *guard) retryAfter(ctx context.Context, key string) (time.Duration, bool) {
	if delay, ok := ratelimiting.RetryAfterFor(ctx, g.limiter, key); ok && delay > 0 {
		return delay, true
	}

	if g.cfg.retryAfter <= 0 {
		return 0, false
	}

	return g.cfg.retryAfter, true
}

package http

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/routing"
)

// serviceName names this component's logger, tracer, and metrics.
const serviceName = "ratelimiting_http"

// ErrNilLimiter indicates NewMiddleware was called without a limiter.
var ErrNilLimiter = platformerrors.New("nil rate limiter")

// middleware holds what every request needs, built once at construction.
type middleware struct {
	limiter ratelimiting.RateLimiter
	key     KeyFunc
	cfg     *config
	o11y    observability.Observer
	enc     encoding.ServerEncoderDecoder

	allowedCounter metrics.Int64Counter
	refusedCounter metrics.Int64Counter
	errorCounter   metrics.Int64Counter
}

// NewMiddleware builds middleware that spends a token per request and answers
// 429 when there is none.
//
// It is the inbound half of what httpclient.WithRateLimit does outbound, over
// the same ratelimiting.RateLimiter — so a service can be configured with one
// limiter provider and have both directions obey it.
//
// keyFn decides what the limit is per: principal, API key, address. There is no
// default, because the wrong one is worse than none. Keying an authenticated
// API on addresses pools an entire office behind one bucket; keying a public
// one on a header the client controls hands out a fresh bucket per request. See
// KeyByRemoteAddr and its neighbors.
//
// Install it globally with Router.Use to protect the whole surface, or per route
// with routing.WithMiddleware where one endpoint is dramatically more expensive
// than the rest. Unlike the idempotency middleware it never reads the request
// body, so a global install costs upload routes nothing.
func NewMiddleware(limiter ratelimiting.RateLimiter, keyFn KeyFunc, opts ...Option) (routing.Middleware, error) {
	if limiter == nil {
		return nil, ErrNilLimiter
	}

	if keyFn == nil {
		return nil, ErrNilKeyFunc
	}

	cfg := newConfig(opts...)

	m := &middleware{
		limiter: limiter,
		key:     keyFn,
		cfg:     cfg,
		o11y:    observability.NewObserver(serviceName, cfg.logger, cfg.tracerProvider),
		enc: encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON,
			encoding.WithLogger(cfg.logger),
			encoding.WithTracerProvider(cfg.tracerProvider)),
	}

	mp := metrics.EnsureMetricsProvider(cfg.metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&m.allowedCounter, "allowed"},
		{&m.refusedCounter, "refused"},
		{&m.errorCounter, "errors"},
	}
	for _, c := range counters {
		// No attributes on any of them, and in particular not the key. The key
		// is a principal or an address, which is unbounded by construction, and
		// a metric attribute per caller is a cardinality incident. Which key was
		// refused goes on the span, where it is one value on one trace.
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", serviceName, c.name))
		if err != nil {
			return nil, platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			m.serve(res, req, next)
		})
	}, nil
}

// serve runs the guard for one request and, if it passes, the handler.
//
// The decision owns the span and the handler runs outside it — deliberately.
// A span held open across next.ServeHTTP would make every trace read as though
// the rate limiter took the whole request, when what it did was one check.
func (m *middleware) serve(res http.ResponseWriter, req *http.Request, next http.Handler) {
	if m.admit(res, req) {
		next.ServeHTTP(res, req)
	}
}

// admit spends a token for req and reports whether it should reach the handler.
// A request it does not admit has already been answered.
func (m *middleware) admit(res http.ResponseWriter, req *http.Request) bool {
	ctx, op := m.o11y.Begin(req.Context())
	defer op.End()

	key, err := m.key(req)
	if err != nil {
		return m.fail(ctx, res, op, err, "extracting the rate limit key")
	}

	if key == "" {
		// The extractor exempted this request. Not counted as allowed: nothing
		// was spent, and folding it in would make the allowed counter a request
		// count rather than a measure of what the limiter let through.
		return true
	}

	// Span only. The key identifies a caller, so it is a per-request value that
	// belongs on the one trace that request produced — not on every log line
	// the middleware writes, and certainly not on a metric attribute.
	op.SpanOnly(keys.RateLimitKeyKey, key)

	allowed, err := m.limiter.Allow(ctx, key)
	if err != nil {
		return m.fail(ctx, res, op, err, "consulting the rate limiter")
	}

	if !allowed {
		m.refuse(ctx, res, op, req, key)

		return false
	}

	m.allowedCounter.Add(ctx, 1)

	return true
}

// refuse answers a request the limiter turned down.
func (m *middleware) refuse(
	ctx context.Context,
	res http.ResponseWriter,
	op observability.Operation,
	req *http.Request,
	key string,
) {
	m.refusedCounter.Add(ctx, 1)

	if delay, ok := m.retryAfter(ctx, key); ok {
		op.SpanOnly(keys.RetryAfterKey, delay.String())
		res.Header().Set(RetryAfterHeader, retryAfterSeconds(delay))
	}

	// Debug rather than error: a limiter refusing a request is the limiter
	// working. The signal an operator watches is the refused counter's rate,
	// not a line per refusal — which under the traffic that triggers this is
	// the log volume the refusal was meant to prevent.
	op.Logger().WithRequest(req).Debug("rate limited")

	// The bare sentinel, unwrapped: whatever the encoder does with it, the
	// message reaching the client must not name the key or the limit.
	m.write(ctx, res, ratelimiting.ErrRateLimited)
}

// fail resolves a request the limiter could not rule on, per WithFailClosed,
// and reports whether it should still reach the handler.
func (m *middleware) fail(
	ctx context.Context,
	res http.ResponseWriter,
	op observability.Operation,
	err error,
	description string,
) bool {
	m.errorCounter.Add(ctx, 1)

	// Error, unlike a refusal: a guard that cannot answer is a fault, and it is
	// invisible in traffic either way it resolves — the fail-open case looks
	// like a quiet limiter and the fail-closed case looks like an outage
	// somewhere else.
	op.Acknowledge(err, "%s", description)

	if !m.cfg.failClosed {
		return true
	}

	m.write(ctx, res, platformerrors.Wrap(ratelimiting.ErrRateLimited, description))

	return false
}

// retryAfter resolves the hint for a refusal: the limiter's own estimate when
// it has one, the configured fallback otherwise, and nothing when that fallback
// has been suppressed.
func (m *middleware) retryAfter(ctx context.Context, key string) (time.Duration, bool) {
	if delay, ok := ratelimiting.RetryAfterFor(ctx, m.limiter, key); ok && delay > 0 {
		return delay, true
	}

	if m.cfg.retryAfter <= 0 {
		return 0, false
	}

	return m.cfg.retryAfter, true
}

// write renders err through the configured ErrorEncoder, or the platform
// envelope when there is none.
//
// It mirrors routing.Router.writeError, including the out-of-range status
// clamp: a custom encoder is caller code, and a status it cannot serve would
// panic the ResponseWriter on a request that was already being refused.
func (m *middleware) write(ctx context.Context, res http.ResponseWriter, err error) {
	encode := m.cfg.errEncoder
	if encode == nil {
		encode = routing.DefaultErrorBody
	}

	status, body := encode(ctx, err)
	if status < 100 || status > 999 {
		status = http.StatusInternalServerError
	}

	if body == nil {
		res.WriteHeader(status)

		return
	}

	m.enc.EncodeResponseWithStatus(ctx, res, body, status)
}

// retryAfterSeconds renders a delay as the header's delta-seconds form.
//
// It rounds up, and never below 1. The header has one-second resolution, so a
// 200ms wait rounded down would be 0 — an invitation to come straight back for
// a token that is not there yet, which is the retry storm the header exists to
// prevent.
func retryAfterSeconds(delay time.Duration) string {
	return strconv.Itoa(max(int(math.Ceil(delay.Seconds())), 1))
}

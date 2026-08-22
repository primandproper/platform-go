package http

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httperrors "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/routing"

	"go.opentelemetry.io/otel/trace"
)

const serviceName = "idempotency_http"

// ErrNilManager indicates NewMiddleware was called without a manager.
var ErrNilManager = platformerrors.New("nil idempotency manager")

// middleware holds what every request needs, built once at construction.
type middleware struct {
	manager *idempotency.Manager[Response]
	cfg     *config
	o11y    observability.Observer
	enc     encoding.ServerEncoderDecoder

	bodyOmittedCounter metrics.Int64Counter
}

// NewMiddleware builds middleware that runs a handler at most once per
// Idempotency-Key.
//
// Requests without the header pass through completely untouched — the body is
// not even read — so installing this can only affect clients that opted in.
//
// Prefer installing it per route with routing.WithMiddleware rather than
// globally with Router.Use. It caps request bodies in order to fingerprint
// them, and a global install would impose that cap on upload routes that never
// asked for it.
func NewMiddleware(manager *idempotency.Manager[Response], opts ...Option) (routing.Middleware, error) {
	if manager == nil {
		return nil, ErrNilManager
	}

	cfg := newConfig(opts...)
	m := &middleware{
		manager: manager,
		cfg:     cfg,
		o11y:    observability.NewObserver(serviceName, cfg.logger, cfg.tracerProvider),
		enc:     encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON, encoding.WithLogger(cfg.logger), encoding.WithTracerProvider(cfg.tracerProvider)),
	}

	// The one instrument this layer owns. Everything else worth watching is
	// counted by the manager, but only this layer knows a body was dropped,
	// and it is the signal that WithMaxResponseBytes is set too low.
	var err error
	if m.bodyOmittedCounter, err = metrics.EnsureMetricsProvider(cfg.metricsProvider).
		NewInt64Counter(fmt.Sprintf("%s_bodies_omitted", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating bodies omitted counter")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			if req.Header.Get(cfg.headerName) == "" || !slices.Contains(cfg.methods, req.Method) {
				next.ServeHTTP(res, req)

				return
			}

			m.serve(res, req, next)
		})
	}, nil
}

// serve handles one keyed request.
func (m *middleware) serve(res http.ResponseWriter, req *http.Request, next http.Handler) {
	ctx, op := m.o11y.Begin(req.Context())
	defer op.End()

	key := idempotency.Key(req.Header.Get(m.cfg.headerName))

	principal, err := m.principal(req)
	if err != nil {
		m.writeError(ctx, res, op, err, "extracting request principal")

		return
	}

	body, err := m.readBody(res, req)
	if err != nil {
		if tooLarge, ok := stderrors.AsType[*http.MaxBytesError](err); ok {
			// Fingerprinting a prefix would let two different requests share a
			// fingerprint, so an oversized body is refused rather than trimmed.
			op.Acknowledge(tooLarge, "reading request body for fingerprinting")
			res.WriteHeader(http.StatusRequestEntityTooLarge)

			return
		}

		m.writeError(ctx, res, op, err, "reading request body")

		return
	}

	fp, err := m.fingerprint(req, principal, body)
	if err != nil {
		m.writeError(ctx, res, op, err, "fingerprinting request")

		return
	}

	// The handler must see the body the client sent. capitalism's Stripe
	// webhook reads req.Body itself and verifies a signature over those bytes,
	// so a body consumed here is a broken handler there.
	inner := req.WithContext(ctx)
	inner.Body = io.NopCloser(bytes.NewReader(body))

	wrapped, rec := newRecorder(res, m.cfg.maxResponseBytes)

	result, err := m.manager.Do(ctx, key, fp, func(context.Context) (*Response, error) {
		next.ServeHTTP(wrapped, inner)

		return rec.response(m.cfg.replayedHeaders), nil
	})
	if err != nil {
		// Safe to write a status here because the handler cannot have written
		// one: the closure above never returns an error, so every error Do can
		// return — key validation, a store read, a mismatch or in-flight
		// record, a failed claim — is decided before the handler runs. A
		// failure after it runs is counted by the manager and swallowed, since
		// the caller is owed the result the work produced.
		//
		// A closure that could return an error would break that, and would
		// need a wroteHeader guard before calling this.
		m.writeError(ctx, res, op, err, "running idempotent handler")

		return
	}

	if !result.Replayed {
		// The handler served the client directly as it ran.
		return
	}

	if result.Value.Truncated {
		m.bodyOmittedCounter.Add(ctx, 1)
	}

	if err = writeResponse(res, result.Value, m.cfg.replayHeader); err != nil {
		op.Acknowledge(err, "writing replayed response")
	}
}

// principal resolves the caller identity folded into the fingerprint.
func (m *middleware) principal(req *http.Request) (string, error) {
	if m.cfg.principal == nil {
		return "", nil
	}

	return m.cfg.principal(req)
}

// fingerprint resolves the request fingerprint, deferring to a caller-supplied
// function when one was configured.
func (m *middleware) fingerprint(req *http.Request, principal string, body []byte) (idempotency.Fingerprint, error) {
	if m.cfg.fingerprint != nil {
		return m.cfg.fingerprint(req, body)
	}

	return fingerprint(req, principal, body), nil
}

// readBody reads the request body under a cap.
//
// res is handed to MaxBytesReader deliberately: it lets the server close the
// connection after replying rather than trying to drain a body it has already
// rejected.
func (m *middleware) readBody(res http.ResponseWriter, req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	return io.ReadAll(http.MaxBytesReader(res, req.Body, m.cfg.maxRequestBody))
}

// writeError maps an error onto the platform's status and error envelope.
//
// The idempotency sentinels are mapped in errors/http, so ErrInFlight becomes
// 409 and ErrFingerprintMismatch 422 without this package restating either.
func (m *middleware) writeError(
	ctx context.Context,
	res http.ResponseWriter,
	op observability.Operation,
	err error,
	description string,
) {
	op.Acknowledge(err, "%s", description)

	code, msg := httperrors.ToAPIError(err)
	status := httperrors.HTTPStatusForCode(code)

	if status == http.StatusConflict {
		// The client has no other signal for when the in-flight work might
		// have finished.
		res.Header().Set("Retry-After", strconv.Itoa(max(1, int(m.cfg.retryAfter.Seconds()))))
	}

	m.enc.EncodeResponseWithStatus(ctx, res, httperrors.NewAPIErrorResponse(msg, code, detailsFromCtx(ctx)), status)
}

// detailsFromCtx builds response details from the active span, matching what
// the router puts on its own error envelopes.
func detailsFromCtx(ctx context.Context) httperrors.ResponseDetails {
	details := httperrors.ResponseDetails{}
	if sc := trace.SpanContextFromContext(ctx); sc.HasTraceID() {
		details.TraceID = sc.TraceID().String()
	}

	return details
}

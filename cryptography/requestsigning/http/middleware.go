package http

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"

	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/routing"
)

// serviceName names this component's logger, tracer, and metrics.
const serviceName = "requestsigning_http"

var (
	// ErrNilVerifier indicates NewMiddleware was called without a verifier.
	ErrNilVerifier = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil request signature verifier")

	// ErrBodyTooLarge indicates a request body past the configured cap. It
	// wraps requestsigning.ErrInvalidSignature, and answers 401 rather than
	// 413, because what actually happened is that the request could not be
	// authenticated — and a distinct status here would tell an unauthenticated
	// caller exactly where the cap sits.
	ErrBodyTooLarge = platformerrors.Wrap(requestsigning.ErrInvalidSignature, "request body exceeds the signable limit")
)

// middleware holds what every request needs, built once at construction.
type middleware struct {
	verifier requestsigning.Verifier
	o11y     observability.Observer
	enc      encoding.ServerEncoderDecoder

	verifiedCounter metrics.Int64Counter
	rejectedCounter metrics.Int64Counter
	errorCounter    metrics.Int64Counter
	cfg             *config
}

// NewMiddleware builds middleware that verifies a request's signature before
// the handler runs and answers 401 when it does not check out.
//
// It is the inbound half of what httpclient.WithRequestSigning does outbound,
// over the same requestsigning.Verifier — so a first-party caller and the
// service it calls can be configured from one scheme and one key source, and a
// third party's scheme plugs into the same seam.
//
// Install it per route with routing.WithMiddleware, not globally with
// Router.Use. Verification requires the whole body in memory before the handler
// sees it, which is the right price on a callback endpoint and the wrong one on
// every upload route in the service. It also fails closed by construction:
// there is no configuration under which an unsigned request reaches the
// handler, because a guard that can be talked out of guarding is not one.
//
//	verifier, err := requestsigning.NewVerifier(keys)
//	if err != nil {
//		return err
//	}
//
//	mw, err := requestsigninghttp.NewMiddleware(verifier,
//		requestsigninghttp.WithMetricsProvider(pillars.Metrics))
//	if err != nil {
//		return err
//	}
//
//	routing.Post(router, "/callbacks/payments", handler, routing.WithMiddleware(mw))
//
// # The body
//
// The handler is handed the same bytes that were verified, replayed from
// memory. That is not a convenience: a handler that re-read the socket, or that
// decoded and re-encoded, would be acting on something other than what the
// signature covered, which is the exact bug this middleware exists to stop
// people writing by hand.
func NewMiddleware(verifier requestsigning.Verifier, opts ...Option) (routing.Middleware, error) {
	if verifier == nil {
		return nil, ErrNilVerifier
	}

	cfg := newConfig(opts...)

	m := &middleware{
		verifier: verifier,
		cfg:      cfg,
		o11y: observability.NewObserverWithValues(serviceName, cfg.logger, cfg.tracerProvider,
			map[string]any{keys.SignatureSchemeKey: verifier.Scheme()}),
		enc: encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON,
			encoding.WithLogger(cfg.logger),
			encoding.WithTracerProvider(cfg.tracerProvider)),
	}

	mp := metrics.EnsureMetricsProvider(cfg.metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&m.verifiedCounter, "verified"},
		{&m.rejectedCounter, "rejected"},
		{&m.errorCounter, "errors"},
	}
	for _, c := range counters {
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

// serve verifies one request and, if it checks out, runs the handler.
//
// The verification owns the span and the handler runs outside it — deliberately.
// A span held open across next.ServeHTTP would make every trace read as though
// signature checking took the whole request, when what it did was one HMAC.
func (m *middleware) serve(res http.ResponseWriter, req *http.Request, next http.Handler) {
	verified, replayed := m.admit(res, req)
	if !verified {
		return
	}

	next.ServeHTTP(res, replayed)
}

// admit verifies req and reports whether it should reach the handler, along
// with the replayable request the handler should get. A request it does not
// admit has already been answered.
func (m *middleware) admit(res http.ResponseWriter, req *http.Request) (verified bool, replayed *http.Request) {
	ctx, op := m.o11y.Begin(req.Context())
	defer op.End()

	body, err := m.read(req)
	if err != nil {
		m.reject(ctx, res, op, req, err)

		return false, nil
	}

	// Rebuilt before verification rather than after, so the verifier and the
	// handler read one request between them — the same bytes, bounded by the
	// same cap, and no opportunity for the two reads to disagree.
	//
	// Onto a shallow copy rather than the request itself, the way
	// http.MaxBytesHandler does it: whoever called this middleware still holds
	// the original, and a body swapped out from under them is a surprise.
	buffered := *req
	buffered.Body = io.NopCloser(bytes.NewReader(body))
	buffered.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	buffered.ContentLength = int64(len(body))

	// GetBody is set above, so the verifier's read rewinds rather than consumes
	// and the handler still gets every byte.
	if err = m.verifier.VerifyRequest(ctx, &buffered); err != nil {
		m.reject(ctx, res, op, req, err)

		return false, nil
	}

	m.verifiedCounter.Add(ctx, 1)

	return true, &buffered
}

// read buffers the request body, refusing to read past the cap.
//
// It reads one byte beyond the limit rather than exactly the limit, because
// those are the only two readings that distinguish "a body of exactly the
// maximum size" from "a body that was truncated" — and silently verifying a
// truncated body would reject every large request as a forgery.
func (m *middleware) read(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(io.LimitReader(req.Body, m.cfg.maxBodySize+1))
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the request body")
	}

	if int64(len(body)) > m.cfg.maxBodySize {
		return nil, ErrBodyTooLarge
	}

	return body, nil
}

// reject answers a request that did not verify.
func (m *middleware) reject(
	ctx context.Context,
	res http.ResponseWriter,
	op observability.Operation,
	req *http.Request,
	err error,
) {
	// A body this middleware could not read is its own bucket: it is not a
	// verdict about the caller's key, and lumping it in with rejections would
	// hide a client hanging up mid-upload inside a signature-failure rate.
	if !stderrors.Is(err, requestsigning.ErrInvalidSignature) && !stderrors.Is(err, requestsigning.ErrStaleSignature) {
		m.errorCounter.Add(ctx, 1)
		op.Acknowledge(err, "verifying the request signature")
	} else {
		m.rejectedCounter.Add(ctx, 1)

		// Info rather than error: a rejection is the guard working, and under
		// the traffic that makes this fire, a line per rejection is the load
		// the rejection was meant to shed. It is above Debug because unlike a
		// rate-limit refusal, a signature that does not verify is either a
		// misconfigured counterparty or somebody trying it on, and both are
		// worth seeing without turning debug on.
		op.Logger().WithRequest(req).WithError(err).Info("rejected an unverified request")
	}

	m.write(ctx, res, err)
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

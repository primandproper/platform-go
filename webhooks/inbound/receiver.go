package inbound

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// credentialHeaders are dropped from Delivery.Headers rather than forwarded.
//
// No webhook provider sends them, which is the point: anything arriving in one
// of these was put there by whoever made the request, and a receiver that
// copied it onto a topic would be publishing a credential — possibly its own,
// if a proxy in front of it adds one — into a place chosen for durability
// rather than for secrecy.
var credentialHeaders = map[string]struct{}{
	"Authorization":       {},
	"Proxy-Authorization": {},
	"Cookie":              {},
}

// Receiver is the HTTP half of inbound webhook handling: it reads a bounded
// body, verifies it, publishes it, and acks.
//
// One Receiver serves one provider endpoint, because it holds one Verifier
// (which knows one scheme and one set of secrets) and one Publisher (which is
// bound to one topic). A service taking webhooks from Stripe and GitHub builds
// two and mounts them at two paths.
//
// It is a concrete type rather than an interface. There is no second
// implementation to swap in: the seams that vary are Verifier and
// messagequeue.Publisher, and both are already interfaces this takes.
type Receiver struct {
	verifier  Verifier
	publisher messagequeue.Publisher
	clock     clock.Clock
	o11y      observability.Observer

	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	receivedCounter  metrics.Int64Counter
	rejectedCounter  metrics.Int64Counter
	publishedCounter metrics.Int64Counter
	ackLatencyHist   metrics.Float64Histogram

	// forwarded is the header allowlist WithForwardedHeaders built, or nil for
	// "everything except credentialHeaders".
	forwarded map[string]struct{}

	providerAttr metric.MeasurementOption
	maxBodyBytes int64
}

var _ http.Handler = (*Receiver)(nil)

// NewReceiver builds a Receiver.
//
// verifier and publisher are parameters rather than options because neither has
// a safe default. A receiver with no verifier is a public endpoint for
// injecting messages onto an internal topic; a receiver with no publisher acks
// deliveries and drops them, which is the failure this package exists to make
// impossible.
func NewReceiver(verifier Verifier, publisher messagequeue.Publisher, opts ...ReceiverOption) (*Receiver, error) {
	if verifier == nil {
		return nil, ErrNilVerifier
	}

	if publisher == nil {
		return nil, ErrNilPublisher
	}

	r := &Receiver{
		verifier:     verifier,
		publisher:    publisher,
		clock:        clock.NewClock(),
		maxBodyBytes: DefaultMaxBodyBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	provider := verifier.Provider()

	// Named for the provider, so every log line and span this receiver emits
	// says which endpoint it came from without each call site restating it.
	r.o11y = observability.NewObserverWithValues(serviceName, r.logger, r.tracerProvider, map[string]any{
		"webhook.provider": provider,
	})
	r.providerAttr = metric.WithAttributes(attribute.String("provider", provider))

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.receivedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_received"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries received counter")
	}
	if r.rejectedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_rejected"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries rejected counter")
	}
	if r.publishedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_published"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries published counter")
	}
	// The histogram this package exists for: everything between the request
	// arriving and the ack going out. A provider's deadline is measured in tens
	// of seconds, and the point of publishing rather than processing is that
	// this distribution stays nowhere near it.
	if r.ackLatencyHist, err = mp.NewFloat64Histogram(serviceName + "_ack_latency_ms"); err != nil {
		return nil, platformerrors.Wrap(err, "creating ack latency histogram")
	}

	return r, nil
}

// Mount registers the receiver as a POST route on router.
//
// It goes through routing.Handle, the untyped escape hatch, so no OpenAPI
// operation is recorded. That is deliberate rather than an omission: the
// request body is opaque provider JSON whose schema this package does not know,
// and a spec claiming otherwise would be documenting a shape nothing enforces.
//
// POST only. Every provider here posts, and accepting other methods would
// widen a public endpoint for no caller.
func (r *Receiver) Mount(router *routing.Router, pattern string, middleware ...routing.Middleware) {
	router.Handle(http.MethodPost, pattern, r, middleware...)
}

// ServeHTTP reads, verifies, publishes, and acks — in that order, and with
// nothing else between the request and the response.
//
// The status codes are chosen for what the provider does with them, which is
// retry anything that is not 2xx:
//
//   - 204 once the delivery is durably published. There is no body, because
//     nothing reads it and an error string on a public endpoint is a probing
//     surface.
//   - 400 for a signature that did not check out, and for a body that could not
//     be read. Neither improves on a retry.
//   - 413 for a body over the cap, so provider-side delivery logs show a size
//     problem rather than a signature problem.
//   - 503 for a failed publish. This is the case the design turns on: the
//     receiver has not acked, so the delivery is still the provider's, and its
//     retry is what covers the outage.
func (r *Receiver) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	ctx, op := r.o11y.BeginCustom(req.Context(), serviceName+".receive")
	defer op.End()

	startedAt := r.clock.Now()
	r.receivedCounter.Add(ctx, 1, r.providerAttr)

	defer func() {
		r.ackLatencyHist.Record(ctx, float64(r.clock.Since(startedAt).Nanoseconds())/float64(time.Millisecond), r.providerAttr)
	}()

	body, err := r.readBody(res, req)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrBodyTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}

		r.reject(ctx, res, op, err, "unreadable_body", status)

		return
	}

	op.Set("webhook.body_bytes", len(body))

	if err = r.verifier.Verify(ctx, req.Header, body); err != nil {
		reason := "invalid_signature"
		if errors.Is(err, ErrStaleSignature) {
			reason = "stale_signature"
		}

		r.reject(ctx, res, op, err, reason, http.StatusBadRequest)

		return
	}

	// Publish, then ack. The order is the guarantee: a delivery this receiver
	// acked is a delivery the broker holds, and one the broker refused is one
	// the provider still owns and will send again.
	//
	// Publish rather than PublishAsync, which only names how errors are
	// reported — it publishes on this goroutine either way, and swallowing the
	// error is exactly what would let a 204 go out over a lost event.
	if err = r.publisher.Publish(ctx, &Delivery{
		Provider:   r.verifier.Provider(),
		Body:       body,
		Headers:    r.forwardedHeaders(req.Header),
		ReceivedAt: startedAt,
	}); err != nil {
		r.reject(ctx, res, op, err, "publish_failed", http.StatusServiceUnavailable)

		return
	}

	r.publishedCounter.Add(ctx, 1, r.providerAttr)
	res.WriteHeader(http.StatusNoContent)
}

// readBody reads the request body under the receiver's cap, distinguishing an
// oversized body from an unreadable one.
//
// The cap is applied here rather than by a middleware because the bytes it
// bounds are the bytes the signature is over: a limit applied somewhere the
// verifier cannot see would silently verify a truncated payload.
func (r *Receiver) readBody(res http.ResponseWriter, req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}

	body, err := io.ReadAll(http.MaxBytesReader(res, req.Body, r.maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, platformerrors.Wrapf(ErrBodyTooLarge, "limit is %d bytes", r.maxBodyBytes)
		}

		return nil, platformerrors.Wrap(err, "reading the request body")
	}

	return body, nil
}

// forwardedHeaders copies the headers a Delivery carries: the allowlist when
// one was configured, everything but credentialHeaders otherwise.
//
// The copy is deep, because the returned value outlives the request: it goes onto a queue,
// while the request's own header map is the server's to reuse once the handler returns.
func (r *Receiver) forwardedHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}

	forwarded := make(http.Header, len(headers))

	for name, values := range headers {
		canonical := http.CanonicalHeaderKey(name)

		if r.forwarded != nil {
			if _, ok := r.forwarded[canonical]; !ok {
				continue
			}
		} else if _, denied := credentialHeaders[canonical]; denied {
			continue
		}

		forwarded[canonical] = append([]string(nil), values...)
	}

	if len(forwarded) == 0 {
		return nil
	}

	return forwarded
}

// reject records a refusal on every pillar and writes its status, with no body.
//
// The reason is set on the operation before the error is, so it reaches the span and the log
// line as well as the metric — one label, three pillars, and no chance of the log saying
// something the counter disagrees with. The error itself carries what actually went wrong.
func (r *Receiver) reject(
	ctx context.Context,
	res http.ResponseWriter,
	op observability.Operation,
	err error,
	reason string,
	status int,
) {
	r.rejectedCounter.Add(ctx, 1, r.providerAttr, metric.WithAttributes(attribute.String("reason", reason)))

	op.Set("webhook.rejection_reason", reason).Set("http.status_code", status)

	// Acknowledge rather than Error: the refusal is the handler's answer, not something to
	// wrap and hand back up. Nothing above ServeHTTP would read the error anyway.
	op.Acknowledge(err, "refusing an inbound webhook")

	res.WriteHeader(status)
}

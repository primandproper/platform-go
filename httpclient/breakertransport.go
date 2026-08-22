package httpclient

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	"github.com/primandproper/platform-go/v13/circuitbreaking/partitioned"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"go.opentelemetry.io/otel/attribute"
)

// breakerTransport consults a per-host circuit breaker before spending a
// connection on a dependency that has been failing.
type breakerTransport struct {
	base       http.RoundTripper
	breakers   partitioned.KeyedCircuitBreaker
	classifier OutcomeClassifier
	obs        *transportObserver
}

var _ http.RoundTripper = (*breakerTransport)(nil)

// BreakerOption tunes the transport WithCircuitBreaker and
// WithKeyedCircuitBreaker install.
type BreakerOption func(*breakerTransport)

// WithOutcomeClassifier replaces the rule deciding what a finished request says
// about the health of the host it was sent to.
//
// The default, DefaultOutcome, counts transport errors and 5xx responses
// against the host and nothing else. That is the right reading of a standard
// HTTP API and the wrong reading of a great many real ones: a service that
// answers 200 with an error document, or 400 for its own overload, or 503 for a
// tenant that is merely out of quota, will either trip a circuit that should
// have stayed closed or hold one closed that should have opened.
//
// A classifier that only wants to reclassify one thing should delegate the rest
// to DefaultOutcome rather than restating it:
//
//	httpclient.WithOutcomeClassifier(func(resp *http.Response, err error) httpclient.Outcome {
//		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
//			return httpclient.OutcomeIgnored
//		}
//
//		return httpclient.DefaultOutcome(resp, err)
//	})
//
// A nil classifier is ignored.
func WithOutcomeClassifier(classifier OutcomeClassifier) BreakerOption {
	return func(t *breakerTransport) {
		if classifier != nil {
			t.classifier = classifier
		}
	}
}

// newBreakerTransport resolves the breaker options into an unattached
// transport. buildClient fills in the base and the observer.
func newBreakerTransport(breakers partitioned.KeyedCircuitBreaker, opts []BreakerOption) *breakerTransport {
	t := &breakerTransport{
		breakers:   breakers,
		classifier: DefaultOutcome,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	return t
}

// RoundTrip fails fast when the host's circuit is open, and reports the outcome
// of every request it does let through.
func (t *breakerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host

	breaker := t.breakers.For(host)
	if breaker == nil {
		return t.base.RoundTrip(req)
	}

	ctx := req.Context()

	if breaker.CannotProceed() {
		t.obs.circuitRejections.Add(ctx, 1, requestAttrs(req))

		// Wrapped rather than replaced: errors/http already maps
		// ErrCircuitBroken, and the host is what makes the log line useful.
		err := platformerrors.Wrapf(circuitbreaking.ErrCircuitBroken, "host %q", host)

		// This is the one failure in the stack that leaves no other trace of
		// itself. The request never reaches the wire, so there is no attempt, no
		// response, and no span from the tracing transport below — a client that
		// has stopped talking to a dependency altogether would otherwise look
		// exactly like one with nothing to say.
		t.obs.o11y.Logger().WithRequest(req).Error("refusing request, circuit is open", err)

		return nil, err
	}

	resp, err := t.base.RoundTrip(req)

	outcome := t.classifier(resp, err)

	t.obs.circuitOutcomes.Add(ctx, 1, requestAttrs(req, attribute.String(keys.OutcomeKey, outcome.String())))

	switch outcome {
	case OutcomeFailure:
		breaker.Failed()
	case OutcomeSuccess:
		breaker.Succeeded()
	case OutcomeIgnored:
		// Recorded above and deliberately not reported to the breaker: the
		// exchange says nothing about whether the host is well.
	}

	return resp, err
}

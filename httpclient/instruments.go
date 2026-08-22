package httpclient

import (
	"fmt"
	"net/http"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// serviceName names this component's logger, tracer, and metrics.
const serviceName = "httpclient"

// transportObserver is the observability the resilience transports share: one
// named Observer and the instruments they record to.
//
// It is shared rather than built per transport so that a retry, the circuit
// outcome that followed it, and the rate-limit refusal underneath all report
// from one instrumentation scope. They describe a single request between them,
// and splitting them across three scopes would make that impossible to see.
//
// It is never nil, and every field in it is safe to record to: a client built
// without observability gets noop implementations rather than a nil check at
// every call site.
type transportObserver struct {
	o11y observability.Observer

	retryAttempts     metrics.Int64Counter
	retriesExhausted  metrics.Int64Counter
	circuitRejections metrics.Int64Counter
	circuitOutcomes   metrics.Int64Counter
	rateLimited       metrics.Int64Counter
	cacheOutcomes     metrics.Int64Counter
	signingFailures   metrics.Int64Counter

	retryAfterWaits metrics.Float64Histogram
}

// newTransportObserver resolves the three pillars and creates every instrument
// the transports record to. Absent pillars become noops, so this succeeds for a
// client that asked for no observability at all.
func newTransportObserver(
	logger logging.Logger,
	tracerProvider tracing.Provider,
	metricsProvider metrics.Provider,
) (*transportObserver, error) {
	obs := &transportObserver{
		o11y: observability.NewObserver(serviceName, logger, tracerProvider),
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&obs.retryAttempts, "retry_attempts"},
		{&obs.retriesExhausted, "retries_exhausted"},
		{&obs.circuitRejections, "circuit_rejections"},
		{&obs.circuitOutcomes, "circuit_outcomes"},
		{&obs.rateLimited, "rate_limited"},
		{&obs.cacheOutcomes, "cache_outcomes"},
		{&obs.signingFailures, "signing_failures"},
	}
	for _, c := range counters {
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", serviceName, c.name))
		if err != nil {
			return nil, platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	retryAfterWaits, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_retry_after_seconds", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating retry_after_seconds histogram")
	}

	obs.retryAfterWaits = retryAfterWaits

	return obs, nil
}

// requestAttrs is the attribute set every instrument here carries, plus whatever
// the call site adds.
//
// Host and method, and deliberately not the URL: a metric attribute has to stay
// bounded, and a path with an ID in it is not. The host is what a dashboard
// wants to group by anyway — these instruments are all about one dependency
// misbehaving, and the dependency is the host.
func requestAttrs(req *http.Request, extra ...attribute.KeyValue) metric.MeasurementOption {
	attrs := make([]attribute.KeyValue, 0, len(extra)+2)
	attrs = append(attrs,
		attribute.String(keys.ServerAddressKey, req.URL.Host),
		attribute.String(keys.RequestMethodKey, req.Method),
	)

	return metric.WithAttributes(append(attrs, extra...)...)
}

// String renders an Outcome for logs, spans, and metric attributes.
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeFailure:
		return "failure"
	case OutcomeIgnored:
		return "ignored"
	default:
		return "unknown"
	}
}

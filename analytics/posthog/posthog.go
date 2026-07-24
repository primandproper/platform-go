package posthog

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v6/analytics"
	"github.com/primandproper/platform-go/v6/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"github.com/posthog/posthog-go"
)

const (
	name = "posthog_event_reporter"
)

var (
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Posthog API token")
)

type (
	// EventReporter is a PostHog-backed EventReporter.
	EventReporter struct {
		o11y           observability.Observer
		client         posthog.Client
		eventCounter   metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		circuitBreaker circuitbreaking.CircuitBreaker
	}
)

// NewPostHogEventReporter returns a new PostHog-backed EventReporter.
func NewPostHogEventReporter(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, apiKey string, circuitBreaker circuitbreaking.CircuitBreaker, configModifiers ...func(*posthog.Config)) (analytics.EventReporter, error) {
	if apiKey == "" {
		return nil, ErrEmptyAPIToken
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	eventCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_events", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating event counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}

	logger = logging.EnsureLogger(logger)

	phc := posthog.Config{Endpoint: "https://app.posthog.com"}
	for _, f := range configModifiers {
		f(&phc)
	}
	// Drive the breaker from delivery outcomes (Enqueue only buffers), unless a
	// config modifier already installed its own callback.
	if phc.Callback == nil {
		phc.Callback = &breakerCallback{
			circuitBreaker: circuitBreaker,
			errorCounter:   errorCounter,
			logger:         logger,
		}
	}

	client, err := posthog.NewWithConfig(apiKey, phc)
	if err != nil {
		return nil, err
	}

	c := &EventReporter{
		o11y:           observability.NewObserver(name, logger, tracerProvider),
		client:         client,
		eventCounter:   eventCounter,
		errorCounter:   errorCounter,
		circuitBreaker: circuitBreaker,
	}

	return c, nil
}

// breakerCallback bridges the PostHog client's asynchronous delivery outcomes to
// the circuit breaker. Enqueue only buffers the event, so the breaker must be
// driven from the background flush's Success/Failure callbacks to reflect real
// delivery health.
type breakerCallback struct {
	circuitBreaker circuitbreaking.CircuitBreaker
	errorCounter   metrics.Int64Counter
	logger         logging.Logger
}

func (cb *breakerCallback) Success(posthog.APIMessage) {
	if cb.circuitBreaker != nil {
		cb.circuitBreaker.Succeeded()
	}
}

func (cb *breakerCallback) Failure(_ posthog.APIMessage, err error) {
	cb.errorCounter.Add(context.Background(), 1)
	if cb.circuitBreaker != nil {
		cb.circuitBreaker.Failed()
	}
	cb.logger.Error("posthog event delivery failed", err)
}

// Close wraps the internal client's Close method.
func (c *EventReporter) Close() {
	if err := c.client.Close(); err != nil {
		c.o11y.Logger().Error("closing connection", err)
	}
}

// AddUser upsert's a user's identity.
func (c *EventReporter) AddUser(ctx context.Context, userID string, properties map[string]any) error {
	ctx, op := c.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.UserIDKey, userID).Set(keys.LengthKey, len(properties))

	if c.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	props := posthog.NewProperties()
	for k, v := range properties {
		props.Set(k, v)
	}

	err := c.client.Enqueue(posthog.Identify{
		DistinctId: userID,
		Properties: props,
	})
	if err != nil {
		c.errorCounter.Add(ctx, 1)
		c.circuitBreaker.Failed()
		return op.Error(err, "enqueueing identify event")
	}

	c.eventCounter.Add(ctx, 1)
	// Delivery success is signaled asynchronously via the client callback, not by a
	// successful enqueue (which only buffers the event).
	return nil
}

// EventOccurred associates events with a user.
func (c *EventReporter) EventOccurred(ctx context.Context, event, userID string, properties map[string]any) error {
	ctx, op := c.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.UserIDKey, userID).Set("event", event).Set(keys.LengthKey, len(properties))

	if c.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	props := posthog.NewProperties()
	for k, v := range properties {
		props.Set(k, v)
	}

	err := c.client.Enqueue(posthog.Capture{
		DistinctId: userID,
		Event:      event,
		Properties: props,
	})
	if err != nil {
		c.errorCounter.Add(ctx, 1)
		c.circuitBreaker.Failed()
		return op.Error(err, "enqueueing capture event")
	}

	c.eventCounter.Add(ctx, 1)
	// Delivery success is signaled asynchronously via the client callback, not by a
	// successful enqueue (which only buffers the event).
	return nil
}

// EventOccurredAnonymous records an event for an anonymous user.
func (c *EventReporter) EventOccurredAnonymous(ctx context.Context, event, anonymousID string, properties map[string]any) error {
	ctx, op := c.o11y.Begin(ctx)
	defer op.End()

	op.Set("anonymous_id", anonymousID).Set("event", event).Set(keys.LengthKey, len(properties))

	if c.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	props := posthog.NewProperties()
	for k, v := range properties {
		props.Set(k, v)
	}

	err := c.client.Enqueue(posthog.Capture{
		DistinctId: anonymousID,
		Event:      event,
		Properties: props,
	})
	if err != nil {
		c.errorCounter.Add(ctx, 1)
		c.circuitBreaker.Failed()
		return op.Error(err, "enqueueing capture event")
	}

	c.eventCounter.Add(ctx, 1)
	// Delivery success is signaled asynchronously via the client callback, not by a
	// successful enqueue (which only buffers the event).
	return nil
}

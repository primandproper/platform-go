package posthog

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/analytics"
	"github.com/primandproper/platform-go/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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
		tracer         tracing.Tracer
		logger         logging.Logger
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

	phc := posthog.Config{Endpoint: "https://app.posthog.com"}
	for _, f := range configModifiers {
		f(&phc)
	}

	client, err := posthog.NewWithConfig(apiKey, phc)
	if err != nil {
		return nil, err
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

	c := &EventReporter{
		tracer:         tracing.NewNamedTracer(tracerProvider, name),
		logger:         logging.NewNamedLogger(logger, name),
		client:         client,
		eventCounter:   eventCounter,
		errorCounter:   errorCounter,
		circuitBreaker: circuitBreaker,
	}

	return c, nil
}

// Close wraps the internal client's Close method.
func (c *EventReporter) Close() {
	if err := c.client.Close(); err != nil {
		c.logger.Error("closing connection", err)
	}
}

// AddUser upsert's a user's identity.
func (c *EventReporter) AddUser(ctx context.Context, userID string, properties map[string]any) error {
	_, span := c.tracer.StartSpan(ctx)
	defer span.End()

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
		return err
	}

	c.eventCounter.Add(ctx, 1)
	c.circuitBreaker.Succeeded()
	return nil
}

// EventOccurred associates events with a user.
func (c *EventReporter) EventOccurred(ctx context.Context, event, userID string, properties map[string]any) error {
	_, span := c.tracer.StartSpan(ctx)
	defer span.End()

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
		return err
	}

	c.eventCounter.Add(ctx, 1)
	c.circuitBreaker.Succeeded()
	return nil
}

// EventOccurredAnonymous records an event for an anonymous user.
func (c *EventReporter) EventOccurredAnonymous(ctx context.Context, event, anonymousID string, properties map[string]any) error {
	_, span := c.tracer.StartSpan(ctx)
	defer span.End()

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
		return err
	}

	c.eventCounter.Add(ctx, 1)
	c.circuitBreaker.Succeeded()
	return nil
}

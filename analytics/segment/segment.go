package segment

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v4/analytics"
	"github.com/primandproper/platform-go/v4/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	segment "github.com/segmentio/analytics-go/v3"
)

const (
	name = "segment_event_reporter"
)

var (
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Segment API token")
)

type (
	// EventReporter is a Segment-backed EventReporter.
	EventReporter struct {
		o11y           observability.Observer
		client         segment.Client
		eventCounter   metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		circuitBreaker circuitbreaking.CircuitBreaker
	}

	// breakerCallback bridges the Segment client's asynchronous delivery outcomes
	// to the circuit breaker. Enqueue only appends to an in-memory buffer, so the
	// breaker has to be driven from the background flush's Success/Failure callbacks
	// to reflect real delivery health rather than "the buffer accepted the message".
	breakerCallback struct {
		circuitBreaker circuitbreaking.CircuitBreaker
		errorCounter   metrics.Int64Counter
		logger         logging.Logger
	}
)

func (cb *breakerCallback) Success(segment.Message) {
	if cb.circuitBreaker != nil {
		cb.circuitBreaker.Succeeded()
	}
}

func (cb *breakerCallback) Failure(_ segment.Message, err error) {
	cb.errorCounter.Add(context.Background(), 1)
	if cb.circuitBreaker != nil {
		cb.circuitBreaker.Failed()
	}
	cb.logger.Error("segment event delivery failed", err)
}

// NewSegmentEventReporter returns a new Segment-backed EventReporter.
func NewSegmentEventReporter(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, apiKey string, circuitBreaker circuitbreaking.CircuitBreaker) (analytics.EventReporter, error) {
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

	client, err := segment.NewWithConfig(apiKey, segment.Config{
		Callback: &breakerCallback{
			circuitBreaker: circuitBreaker,
			errorCounter:   errorCounter,
			logger:         logger,
		},
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating segment client")
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

	t := segment.NewTraits()
	for k, v := range properties {
		t.Set(k, v)
	}

	i := segment.NewIntegrations().EnableAll()

	err := c.client.Enqueue(segment.Identify{
		UserId:       userID,
		Traits:       t,
		Integrations: i,
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
	return c.eventOccurred(ctx, event, userID, false, properties)
}

// EventOccurredAnonymous records an event for an anonymous user.
func (c *EventReporter) EventOccurredAnonymous(ctx context.Context, event, anonymousID string, properties map[string]any) error {
	return c.eventOccurred(ctx, event, anonymousID, true, properties)
}

func (c *EventReporter) eventOccurred(ctx context.Context, event, userID string, anonymous bool, properties map[string]any) error {
	ctx, op := c.o11y.Begin(ctx)
	defer op.End()

	op.Set("event", event).Set(keys.UserIDKey, userID).Set(keys.LengthKey, len(properties)).Set("anonymous", anonymous)

	if c.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	p := segment.NewProperties()
	for k, v := range properties {
		p.Set(k, v)
	}

	track := segment.Track{
		Event:        event,
		Properties:   p,
		Integrations: segment.NewIntegrations().EnableAll(),
	}

	if anonymous {
		track.AnonymousId = userID
	} else {
		track.UserId = userID
	}

	if err := c.client.Enqueue(track); err != nil {
		c.errorCounter.Add(ctx, 1)
		c.circuitBreaker.Failed()
		return op.Error(err, "enqueueing track event")
	}

	c.eventCounter.Add(ctx, 1)
	// Delivery success is signaled asynchronously via the client callback, not by a
	// successful enqueue (which only buffers the event).
	return nil
}

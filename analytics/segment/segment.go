package segment

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	segment "github.com/segmentio/analytics-go/v3"
)

const (
	name = "segment_event_reporter"
)

var (
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Segment API token")
)

var _ analytics.EventReporter = (*EventReporter)(nil)

type (
	// EventReporter is the Segment analytics.EventReporter implementation. It is
	// exported, and returned by NewEventReporter, so a caller who has chosen
	// Segment can depend on that choice rather than on the interface every
	// reporter shares.
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

// messageIdentity describes a message well enough to say which one was lost,
// without emitting its properties or traits — those are the caller's data, and a
// delivery failure is not a reason to copy them into this service's logs.
func messageIdentity(msg segment.Message) map[string]any {
	values := map[string]any{"message.kind": fmt.Sprintf("%T", msg)}

	switch m := msg.(type) {
	case segment.Track:
		values["message.event"] = m.Event
		values["message.id"] = m.MessageId
	case segment.Identify:
		values["message.id"] = m.MessageId
	case segment.Page:
		values["message.event"] = m.Name
		values["message.id"] = m.MessageId
	case segment.Group:
		values["message.id"] = m.MessageId
	}

	return values
}

// Failure records a delivery the background flush could not complete.
//
// The message is named rather than discarded. This is the only notification a
// caller ever gets that an event did not arrive — Enqueue returned successfully
// long ago — so a log line saying that something failed, without saying what,
// left no way to tell which events are missing from the destination.
func (cb *breakerCallback) Failure(msg segment.Message, err error) {
	cb.errorCounter.Add(context.Background(), 1)
	if cb.circuitBreaker != nil {
		cb.circuitBreaker.Failed()
	}
	cb.logger.WithValues(messageIdentity(msg)).Error("segment event delivery failed", err)
}

// NewEventReporter returns a new Segment-backed EventReporter.
func NewEventReporter(apiKey string, circuitBreaker circuitbreaking.CircuitBreaker, opts ...Option) (*EventReporter, error) {
	if apiKey == "" {
		return nil, ErrEmptyAPIToken
	}

	o := newOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	eventCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_events", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating event counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}

	logger := logging.EnsureLogger(o.logger)

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
		o11y:           observability.NewObserver(name, logger, o.tracerProvider),
		client:         client,
		eventCounter:   eventCounter,
		errorCounter:   errorCounter,
		circuitBreaker: circuitBreaker,
	}

	return c, nil
}

// Close wraps the internal client's Close method.
func (c *EventReporter) Close(ctx context.Context) error {
	_, op := c.o11y.Begin(ctx)
	defer op.End()

	// The error is returned rather than only logged: this is the final flush of
	// whatever is still buffered, and a caller shutting down is the last chance
	// anyone has to notice those events did not make it.
	if err := c.client.Close(); err != nil {
		return observability.PrepareError(err, op.Span(), "closing connection")
	}

	return nil
}

// AddUser upsert's a user's identity.
func (c *EventReporter) AddUser(ctx context.Context, userID string, properties map[string]any) error {
	ctx, op := c.o11y.Begin(ctx,
		observability.WithValue(keys.UserIDKey, userID),
		observability.WithValue(keys.LengthKey, len(properties)),
	)
	defer op.End()

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
	ctx, op := c.o11y.Begin(ctx,
		observability.WithValue("event", event),
		observability.WithValue(keys.UserIDKey, userID),
		observability.WithValue(keys.LengthKey, len(properties)),
		observability.WithValue("anonymous", anonymous),
	)
	defer op.End()

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

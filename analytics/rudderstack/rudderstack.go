package rudderstack

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/analytics"
	"github.com/primandproper/platform-go/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	rudderstack "github.com/rudderlabs/analytics-go/v4"
)

const (
	name = "rudderstack_event_reporter"
)

var (
	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("nil config")
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Rudderstack API token")
	// ErrEmptyDataPlaneURL indicates an empty data plane URL was provided.
	ErrEmptyDataPlaneURL = platformerrors.New("empty data plane URL")
)

type (
	// EventReporter is a Segment-backed EventReporter.
	EventReporter struct {
		tracer         tracing.Tracer
		logger         logging.Logger
		client         rudderstack.Client
		eventCounter   metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		circuitBreaker circuitbreaking.CircuitBreaker
	}
)

// NewRudderstackEventReporter returns a new Segment-backed EventReporter.
func NewRudderstackEventReporter(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, cfg *Config, circuitBreaker circuitbreaking.CircuitBreaker) (analytics.EventReporter, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.APIKey == "" {
		return nil, ErrEmptyAPIToken
	}

	if cfg.DataPlaneURL == "" {
		return nil, ErrEmptyDataPlaneURL
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
		client:         rudderstack.New(cfg.APIKey, cfg.DataPlaneURL),
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

	t := rudderstack.NewTraits()
	for k, v := range properties {
		t.Set(k, v)
	}

	i := rudderstack.NewIntegrations().EnableAll()

	err := c.client.Enqueue(rudderstack.Identify{
		UserId:       userID,
		Traits:       t,
		Integrations: i,
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
	return c.eventOccurred(ctx, event, userID, false, properties)
}

// EventOccurredAnonymous records an event for an anonymous user.
func (c *EventReporter) EventOccurredAnonymous(ctx context.Context, event, anonymousID string, properties map[string]any) error {
	return c.eventOccurred(ctx, event, anonymousID, true, properties)
}

// EventOccurred associates events with a user.
func (c *EventReporter) eventOccurred(ctx context.Context, event, userID string, anonymous bool, properties map[string]any) error {
	_, span := c.tracer.StartSpan(ctx)
	defer span.End()

	if c.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	p := rudderstack.NewProperties()
	for k, v := range properties {
		p.Set(k, v)
	}

	i := rudderstack.NewIntegrations().EnableAll()

	track := rudderstack.Track{
		Event:        event,
		Properties:   p,
		Integrations: i,
	}

	if anonymous {
		track.AnonymousId = userID
	} else {
		track.UserId = userID
	}

	if err := c.client.Enqueue(track); err != nil {
		c.errorCounter.Add(ctx, 1)
		c.circuitBreaker.Failed()
		return err
	}

	c.eventCounter.Add(ctx, 1)
	c.circuitBreaker.Succeeded()
	return nil
}

package saga

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/outbox"
)

// DefaultEventTopic is the outbox topic lifecycle events are published to when
// no other is configured.
const DefaultEventTopic = "saga.lifecycle"

// EventType names what happened to an instance.
type EventType string

const (
	// EventStarted is emitted when an instance is created, in the same
	// transaction that creates it.
	EventStarted EventType = "saga.started"

	// EventStepCompleted is emitted after each successful Do. Step and
	// StepIndex name the step that just finished.
	EventStepCompleted EventType = "saga.step_completed"

	// EventCompleted is emitted when the last step succeeds.
	EventCompleted EventType = "saga.completed"

	// EventCompensating is emitted once, when a saga gives up going forward.
	// Step names the step that failed and Error says why.
	EventCompensating EventType = "saga.compensating"

	// EventStepCompensated is emitted after each successful Undo.
	EventStepCompensated EventType = "saga.step_compensated"

	// EventCompensated is emitted when an instance has unwound every step.
	EventCompensated EventType = "saga.compensated"

	// EventStuck is emitted when an instance reaches StatusStuck. It is the one
	// to route to a pager: something is half-done and this process has run out
	// of ways to undo it.
	EventStuck EventType = "saga.stuck"
)

// Event is one lifecycle notification.
//
// It deliberately carries no saga state. T is the application's own domain
// object and may hold anything — a card token, an address, a prompt — and a
// lifecycle event fans out to every subscriber of a topic. Subscribers that
// need the state have the instance ID and a Runner to read it with.
type Event struct {
	// OccurredAt is when the transition happened, on the worker's clock.
	OccurredAt time.Time `json:"occurredAt"`

	// InstanceID identifies the instance.
	InstanceID string `json:"instanceID"`

	// Definition names the definition being run.
	Definition string `json:"definition"`

	// Step names the step the event is about, where one applies.
	Step string `json:"step,omitempty"`

	// Error is the rendered, truncated cause, set on the events that have one.
	Error string `json:"error,omitempty"`

	// Type says what happened.
	Type EventType `json:"type"`

	// Status is the instance's status after the transition.
	Status Status `json:"status"`

	// StepIndex is the cursor the event is about, or -1 where no step applies.
	StepIndex int `json:"stepIndex"`
}

// EventPublisher receives lifecycle events.
//
// It takes the caller's executor because every event this package emits
// describes a row it is writing in the same transaction. An event that survives
// a rolled-back advance announces a step that did not happen, and a step that
// happened without its event leaves a subscriber waiting forever — the outbox
// pattern exists precisely to have neither, and this seam is what keeps the
// choice in the application's hands.
//
// It is an interface rather than a hard dependency on outbox.Writer so that an
// application already publishing its own events, or one that wants none, is not
// made to adopt a second outbox table.
type EventPublisher interface {
	Publish(ctx context.Context, q database.SQLQueryExecutor, events ...Event) error
}

// EventPublisherFunc adapts a function to EventPublisher.
type EventPublisherFunc func(ctx context.Context, q database.SQLQueryExecutor, events ...Event) error

// Publish implements EventPublisher.
func (f EventPublisherFunc) Publish(ctx context.Context, q database.SQLQueryExecutor, events ...Event) error {
	return f(ctx, q, events...)
}

// OutboxPublisher publishes lifecycle events through an outbox.Writer. It is
// exported, and returned by NewOutboxPublisher, so a caller who has chosen
// outbox delivery can depend on that choice rather than on the EventPublisher
// seam.
type OutboxPublisher struct {
	writer *outbox.Writer
	topic  string
}

var _ EventPublisher = (*OutboxPublisher)(nil)

// NewOutboxPublisher builds an EventPublisher over an outbox.Writer, so
// lifecycle events commit with the instance row they describe and are relayed
// to the broker afterward.
//
// Every event for one instance is enqueued under that instance's ID as the
// outbox key, so the relay keeps them in order relative to one another: a
// subscriber never sees "completed" before "step completed". Events for
// different instances are unordered with respect to each other, which is what
// lets the relay make progress on a thousand sagas without serializing them.
func NewOutboxPublisher(writer *outbox.Writer, opts ...OutboxPublisherOption) (*OutboxPublisher, error) {
	if writer == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil saga outbox writer")
	}

	p := &OutboxPublisher{writer: writer, topic: DefaultEventTopic}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	return p, nil
}

// Publish implements EventPublisher.
func (p *OutboxPublisher) Publish(ctx context.Context, q database.SQLQueryExecutor, events ...Event) error {
	if len(events) == 0 {
		return nil
	}

	msgs := make([]outbox.Message, 0, len(events))
	for i := range events {
		msgs = append(msgs, outbox.Message{
			Topic:   p.topic,
			Key:     events[i].InstanceID,
			Payload: events[i],
		})
	}

	return p.writer.Enqueue(ctx, q, msgs...)
}

// publish sends events through the configured publisher, if there is one.
//
// A publish failure is returned rather than swallowed, unlike a notification
// failure elsewhere in this module. The events go out in the transaction that
// records the progress they describe, so failing the write is what keeps the
// two consistent: the step will be retried, and its idempotency key is what
// stops the retry from doing the work twice.
func publish(ctx context.Context, publisher EventPublisher, q database.SQLQueryExecutor, events ...Event) error {
	if publisher == nil || len(events) == 0 {
		return nil
	}

	return publisher.Publish(ctx, q, events...)
}

// newEvent builds an event describing an instance's current position.
func newEvent(t EventType, inst *Record, step string, stepIndex int, at time.Time, cause string) Event {
	return Event{
		OccurredAt: at.UTC(),
		InstanceID: inst.ID,
		Definition: inst.Definition,
		Step:       step,
		Error:      cause,
		Type:       t,
		Status:     inst.Status,
		StepIndex:  stepIndex,
	}
}

package messagequeue

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

type (
	// Publisher writes messages onto a queue.
	//
	// # Per-message options
	//
	// Both methods take PublishOptions, which carry what the message is rather
	// than how the publisher is built — today the ordering key and the
	// deduplication key. They are per call because one publisher serves every
	// entity on its topic, and the key belongs to the entity.
	//
	// An option a backend has no concept for is ignored rather than rejected, so
	// that a caller can pass the same options to whichever publisher it was
	// wired with. PublishOptions documents, field by field, which backends
	// honor what.
	Publisher interface {
		// Stop halts all publishing.
		Stop()
		// Publish writes a message onto a message queue.
		Publish(ctx context.Context, data any, opts ...PublishOption) error
		// PublishAsync writes a message onto a message queue, logging any error
		// instead of returning it.
		//
		// "Async" names the error handling, not the delivery: it publishes on the
		// calling goroutine and returns when the publish has finished, exactly as
		// Publish does. A caller that wants the publish off its own goroutine has
		// to arrange that itself.
		PublishAsync(ctx context.Context, data any, opts ...PublishOption)
	}

	// PublisherProvider is a function that provides a Publisher for a given topic.
	PublisherProvider interface {
		Close()
		Ping(ctx context.Context) error
		NewPublisher(ctx context.Context, topic string) (Publisher, error)
	}
)

var (
	// ErrEmptyTopicName is returned when a topic name is empty.
	ErrEmptyTopicName = platformerrors.New("empty topic name")

	// ErrConsumerAlreadyRegistered is returned when a second consumer is
	// requested for a topic a provider already has one for.
	//
	// Providers cache consumers by topic, and the cache used to win silently:
	// the second caller got the first caller's consumer, wired to the first
	// caller's handler, and their own handler was never invoked for any message.
	// Nothing failed and nothing logged — the messages simply went somewhere else.
	//
	// One consumer per topic per provider is the rule; a caller that wants two
	// behaviors for one topic multiplexes inside its own handler.
	ErrConsumerAlreadyRegistered = platformerrors.New("a consumer is already registered for this topic")
)

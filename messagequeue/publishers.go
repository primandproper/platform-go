package messagequeue

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v3/errors"
)

type (
	// Publisher produces events onto a queue.
	Publisher interface {
		// Stop halts all publishing.
		Stop()
		// Publish writes a message onto a message queue.
		Publish(ctx context.Context, data any) error
		// PublishAsync writes a message onto a message queue, but logs any encountered errors instead of returning them.
		PublishAsync(ctx context.Context, data any)
	}

	// PublisherProvider is a function that provides a Publisher for a given topic.
	PublisherProvider interface {
		Close()
		Ping(ctx context.Context) error
		ProvidePublisher(ctx context.Context, topic string) (Publisher, error)
	}
)

var (
	// ErrEmptyTopicName is returned when a topic name is empty.
	ErrEmptyTopicName = platformerrors.New("empty topic name")
)

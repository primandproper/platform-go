package messagequeue

import (
	"context"
)

type (
	// Consumer produces events onto a queue.
	Consumer interface {
		Consume(ctx context.Context, stopChan chan bool, errors chan error)
	}

	// ConsumerFunc is a function type that handles consumed messages.
	ConsumerFunc func(context.Context, []byte) error

	// ConsumerProvider is a function that provides a Consumer for a given topic.
	ConsumerProvider interface {
		Close()
		ProvideConsumer(ctx context.Context, topic string, handlerFunc ConsumerFunc) (Consumer, error)
	}
)

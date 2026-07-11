package noop

import (
	"context"

	"github.com/primandproper/platform-go/v4/messagequeue"
)

var (
	_ messagequeue.PublisherProvider = (*publisherProvider)(nil)
	_ messagequeue.Publisher         = (*publisher)(nil)
	_ messagequeue.ConsumerProvider  = (*consumerProvider)(nil)
	_ messagequeue.Consumer          = (*consumer)(nil)
)

// publisherProvider is a no-op implementation of PublisherProvider.
type publisherProvider struct{}

// NewPublisherProvider returns a no-op PublisherProvider.
func NewPublisherProvider() messagequeue.PublisherProvider {
	return &publisherProvider{}
}

func (n *publisherProvider) Close() {}

func (n *publisherProvider) Ping(context.Context) error { return nil }

func (n *publisherProvider) NewPublisher(context.Context, string) (messagequeue.Publisher, error) {
	return NewPublisher(), nil
}

// publisher is a no-op implementation of Publisher.
type publisher struct{}

// NewPublisher returns a no-op Publisher.
func NewPublisher() messagequeue.Publisher {
	return &publisher{}
}

func (n *publisher) Stop() {}

func (n *publisher) Publish(context.Context, any) error {
	return nil
}

func (n *publisher) PublishAsync(context.Context, any) {}

// consumerProvider is a no-op implementation of ConsumerProvider.
type consumerProvider struct{}

// NewConsumerProvider returns a no-op ConsumerProvider.
func NewConsumerProvider() messagequeue.ConsumerProvider {
	return &consumerProvider{}
}

func (n *consumerProvider) Close() {}

func (n *consumerProvider) NewConsumer(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	return NewConsumer(), nil
}

// consumer is a no-op implementation of Consumer.
type consumer struct{}

// NewConsumer returns a no-op Consumer.
func NewConsumer() messagequeue.Consumer {
	return &consumer{}
}

func (n *consumer) Consume(context.Context, chan bool, chan error) {}

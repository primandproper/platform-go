// Package noop is the messagequeue publisher and consumer pair for a service
// with no broker. Publish and PublishAsync accept every message and drop it,
// every messagequeue.PublishOption included, and the providers hand back
// further noops, so a wiring graph that fans out into a dozen topics builds
// without a queue behind any of them.
//
// Consume keeps the messagequeue.Consumer contract: it runs until ctx is done.
// There is nothing to poll, so it blocks on the context and nothing else — no
// handler is ever invoked and nothing is ever sent on errs — but a service that
// blocks on Consume as its run loop serves until it is cancelled, exactly as it
// would with a real broker, rather than exiting at startup.
//
// messagequeue/config builds either provider for the "noop" provider name,
// which has to be given.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/messagequeue"
)

var (
	_ messagequeue.PublisherProvider = (*PublisherProvider)(nil)
	_ messagequeue.Publisher         = (*Publisher)(nil)
	_ messagequeue.ConsumerProvider  = (*ConsumerProvider)(nil)
	_ messagequeue.Consumer          = (*Consumer)(nil)
)

// PublisherProvider is the no-op messagequeue.PublisherProvider.
type PublisherProvider struct{}

// NewPublisherProvider returns a no-op PublisherProvider.
func NewPublisherProvider() *PublisherProvider {
	return &PublisherProvider{}
}

func (n *PublisherProvider) Close() {}

func (n *PublisherProvider) Ping(context.Context) error { return nil }

func (n *PublisherProvider) NewPublisher(context.Context, string) (messagequeue.Publisher, error) {
	return NewPublisher(), nil
}

// Publisher is the no-op messagequeue.Publisher.
type Publisher struct{}

// NewPublisher returns a no-op Publisher.
func NewPublisher() *Publisher {
	return &Publisher{}
}

func (n *Publisher) Stop() {}

func (n *Publisher) Publish(context.Context, any, ...messagequeue.PublishOption) error {
	return nil
}

func (n *Publisher) PublishAsync(context.Context, any, ...messagequeue.PublishOption) {}

// ConsumerProvider is the no-op messagequeue.ConsumerProvider.
type ConsumerProvider struct{}

// NewConsumerProvider returns a no-op ConsumerProvider.
func NewConsumerProvider() *ConsumerProvider {
	return &ConsumerProvider{}
}

func (n *ConsumerProvider) Close() {}

func (n *ConsumerProvider) NewConsumer(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	return NewConsumer(), nil
}

// Consumer is the no-op messagequeue.Consumer.
type Consumer struct{}

// NewConsumer returns a no-op Consumer.
func NewConsumer() *Consumer {
	return &Consumer{}
}

// Consume blocks until ctx is done, which is the messagequeue.Consumer
// contract. It has nothing to poll, so blocking on the context is all it does:
// the handler is never called and errs is never written to.
//
// It matters that it blocks rather than returning. A service whose run loop is
// Consume treats a return as "the consumer stopped", so a noop that returned
// immediately took the process down at startup — with no error to explain it,
// because there was no error.
func (n *Consumer) Consume(ctx context.Context, _ chan<- error) {
	<-ctx.Done()
}

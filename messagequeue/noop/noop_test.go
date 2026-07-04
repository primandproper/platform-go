package noop

import (
	"context"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestPublisherProvider_ProvidePublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisherProvider()
		pub, err := p.ProvidePublisher(context.Background(), "topic")
		must.NoError(t, err)
		test.NotNil(t, pub)
	})
}

func TestPublisherProvider_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisherProvider()
		p.Close()
	})
}

func TestPublisherProvider_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisherProvider()
		err := p.Ping(context.Background())
		test.NoError(t, err)
	})
}

func TestPublisher_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisher()
		err := p.Publish(context.Background(), "data")
		test.NoError(t, err)
	})
}

func TestPublisher_PublishAsync(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisher()
		p.PublishAsync(context.Background(), "data")
	})
}

func TestPublisher_Stop(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisher()
		p.Stop()
	})
}

func TestConsumerProvider_ProvideConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewConsumerProvider()
		c, err := p.ProvideConsumer(context.Background(), "topic", func(_ context.Context, _ []byte) error { return nil })
		must.NoError(t, err)
		test.NotNil(t, c)
	})
}

func TestConsumer_Consume(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c := NewConsumer()
		c.Consume(context.Background(), make(chan bool), make(chan error))
	})
}

func TestConsumerProvider_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewConsumerProvider()
		p.Close()
	})
}

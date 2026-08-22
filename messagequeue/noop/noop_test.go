package noop

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/messagequeue"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestPublisherProvider_NewPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewPublisherProvider()
		pub, err := p.NewPublisher(context.Background(), "topic")
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

	T.Run("accepts and drops publish options", func(t *testing.T) {
		t.Parallel()

		p := NewPublisher()
		err := p.Publish(context.Background(), "data",
			messagequeue.WithOrderingKey("account_123"),
			messagequeue.WithDeduplicationKey("event_456"),
		)
		test.NoError(t, err)

		p.PublishAsync(context.Background(), "data", messagequeue.WithOrderingKey("account_123"))
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

func TestConsumerProvider_NewConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := NewConsumerProvider()
		c, err := p.NewConsumer(context.Background(), "topic", func(_ context.Context, _ []byte) error { return nil })
		must.NoError(t, err)
		test.NotNil(t, c)
	})
}

func TestConsumer_Consume(T *testing.T) {
	T.Parallel()

	T.Run("returns once the context is cancelled", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		returned := make(chan struct{})

		c := NewConsumer()
		go func() {
			defer close(returned)
			c.Consume(ctx, make(chan error))
		}()

		cancel()

		select {
		case <-returned:
		case <-time.After(time.Second):
			t.Fatal("Consume did not return after its context was cancelled")
		}
	})

	T.Run("blocks while the context is live", func(t *testing.T) {
		t.Parallel()

		returned := make(chan struct{})

		c := NewConsumer()
		go func() {
			defer close(returned)
			c.Consume(t.Context(), make(chan error))
		}()

		// The messagequeue.Consumer contract is that Consume runs until ctx is
		// done, and a service whose run loop is Consume reads a return as "the
		// consumer stopped". This one used to return at once, taking that
		// service down at startup with nothing to explain it.
		select {
		case <-returned:
			t.Fatal("Consume returned while its context was still live")
		case <-time.After(100 * time.Millisecond):
		}
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

package asynccfg

import (
	"context"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v10/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v10/messagequeue/mock"
	"github.com/primandproper/platform-go/v10/notifications/async/ably"
	"github.com/primandproper/platform-go/v10/notifications/async/fanout"
	"github.com/primandproper/platform-go/v10/notifications/async/pusher"
	asyncws "github.com/primandproper/platform-go/v10/notifications/async/websocket"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderNoop,
		}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "invalid",
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("pusher requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPusher,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("ably requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAbly,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("websocket requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderWebSocket,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_NewAsyncNotifier(T *testing.T) {
	T.Parallel()

	T.Run("with websocket", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderWebSocket,
			WebSocket: &asyncws.Config{},
		}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with sse", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSSE,
		}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with pusher", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPusher,
			Pusher: &pusher.Config{
				AppID:   "123",
				Key:     "key",
				Secret:  "secret",
				Cluster: "us2",
			},
		}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with ably", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAbly,
			Ably: &ably.Config{
				APIKey: "appid.keyid:keysecret",
			},
		}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	noopProviders := []string{ProviderNoop}
	for _, provider := range noopProviders {
		T.Run(fmt.Sprintf("with noop provider %q", provider), func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Provider: provider,
			}

			actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
			test.NotNil(t, actual)
			test.NoError(t, err)
		})
	}

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil, nil)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

func TestNewAsyncNotifierFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderNoop,
		}

		actual, err := NewAsyncNotifier(t.Context(), cfg, nil, nil, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := NewAsyncNotifier(t.Context(), cfg, nil, nil, nil)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

func TestConfig_fanOut(T *testing.T) {
	T.Parallel()

	selfHosted := []string{ProviderSSE, ProviderWebSocket}
	for _, provider := range selfHosted {
		T.Run(fmt.Sprintf("validates for self-hosted provider %q", provider), func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Provider:  provider,
				WebSocket: &asyncws.Config{},
				FanOut:    &fanout.Config{Enabled: true, Topic: "topic"},
			}

			must.NoError(t, cfg.ValidateWithContext(t.Context()))
		})
	}

	hosted := []string{ProviderPusher, ProviderAbly, ProviderNoop}
	for _, provider := range hosted {
		T.Run(fmt.Sprintf("rejects provider %q", provider), func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Provider: provider,
				Pusher:   &pusher.Config{AppID: "123", Key: "key", Secret: "secret", Cluster: "us2"},
				Ably:     &ably.Config{APIKey: "appid.keyid:keysecret"},
				FanOut:   &fanout.Config{Enabled: true, Topic: "topic"},
			}

			test.ErrorIs(t, cfg.ValidateWithContext(t.Context()), ErrFanOutNotApplicable)

			// And again at construction, because nothing forces a caller to
			// validate first and the failure mode is silent either way.
			actual, err := cfg.NewAsyncNotifier(t.Context(), newPublisherProviderForTest(), newConsumerProviderForTest())
			test.Nil(t, actual)
			test.ErrorIs(t, err, ErrFanOutNotApplicable)
		})
	}

	T.Run("validates once defaults have been applied", func(t *testing.T) {
		t.Parallel()

		// The ordinary case: switched on, topic left to the default. Validating
		// before defaulting would reject it.
		cfg := &Config{Provider: ProviderSSE, FanOut: &fanout.Config{Enabled: true}}
		cfg.EnsureDefaults()

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
		test.EqOp(t, fanout.DefaultTopic, cfg.FanOut.Topic)
	})

	T.Run("is off by default", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSE, FanOut: &fanout.Config{}}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil)
		must.NoError(t, err)

		// No backplane means no messagequeue providers were needed, which is
		// what lets a single-replica deployment pay nothing.
		_, wrapped := actual.(*fanout.Notifier)
		test.False(t, wrapped)
	})

	T.Run("wraps the self-hosted notifier when enabled", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSE, FanOut: &fanout.Config{Enabled: true}}

		actual, err := cfg.NewAsyncNotifier(t.Context(), newPublisherProviderForTest(), newConsumerProviderForTest())
		must.NoError(t, err)

		_, wrapped := actual.(*fanout.Notifier)
		must.True(t, wrapped)

		test.NoError(t, actual.Close())
	})

	T.Run("with missing messagequeue providers", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSE, FanOut: &fanout.Config{Enabled: true}}

		actual, err := cfg.NewAsyncNotifier(t.Context(), nil, nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, fanout.ErrNilPublisherProvider)
	})
}

func newPublisherProviderForTest() messagequeue.PublisherProvider {
	return &messagequeuemock.PublisherProviderMock{
		NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
			return &messagequeuemock.PublisherMock{StopFunc: func() {}}, nil
		},
	}
}

func newConsumerProviderForTest() messagequeue.ConsumerProvider {
	return &messagequeuemock.ConsumerProviderMock{
		NewConsumerFunc: func(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
			return &messagequeuemock.ConsumerMock{
				ConsumeFunc: func(ctx context.Context, _ chan<- error) { <-ctx.Done() },
			}, nil
		},
	}
}

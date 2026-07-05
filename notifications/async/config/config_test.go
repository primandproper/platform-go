package asynccfg

import (
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v4/notifications/async/ably"
	"github.com/primandproper/platform-go/v4/notifications/async/pusher"
	asyncws "github.com/primandproper/platform-go/v4/notifications/async/websocket"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

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

func TestConfig_ProvideAsyncNotifier(T *testing.T) {
	T.Parallel()

	T.Run("with websocket", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderWebSocket,
			WebSocket: &asyncws.Config{},
		}

		actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with sse", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSSE,
		}

		actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
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

		actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
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

		actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	noopProviders := []string{"", ProviderNoop}
	for _, provider := range noopProviders {
		T.Run(fmt.Sprintf("with noop provider %q", provider), func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Provider: provider,
			}

			actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
			test.NotNil(t, actual)
			test.NoError(t, err)
		})
	}

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := cfg.ProvideAsyncNotifier(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

func TestProvideAsyncNotifierFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderNoop,
		}

		actual, err := ProvideAsyncNotifierFromConfig(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := ProvideAsyncNotifierFromConfig(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

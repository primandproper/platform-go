package config

import (
	"testing"

	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("SSE provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderSSE,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("WebSocket provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderWebSocket,
		}

		test.Error(t, cfg.ValidateWithContext(ctx), test.Sprintf("websocket provider requires websocket config"))
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "invalid",
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestProvideEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("SSE", func(t *testing.T) {
		t.Parallel()

		upgrader, err := ProvideEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			Provider: ProviderSSE,
		})

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("WebSocket", func(t *testing.T) {
		t.Parallel()

		upgrader, err := ProvideEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			Provider: ProviderWebSocket,
		})

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{})

		test.Error(t, err)
	})
}

func TestProvideBidirectionalEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("SSE returns error", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideBidirectionalEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			Provider: ProviderSSE,
		})

		test.Error(t, err)
		test.StrContains(t, err.Error(), "SSE does not support bidirectional")
	})

	T.Run("WebSocket", func(t *testing.T) {
		t.Parallel()

		upgrader, err := ProvideBidirectionalEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			Provider: ProviderWebSocket,
		})

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := ProvideBidirectionalEventStreamUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{})

		test.Error(t, err)
	})
}

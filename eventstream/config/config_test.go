package eventstreamcfg

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

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

	// Every field of websocket.Config has a default and NewUpgrader documents a
	// nil config as "use them", so naming the provider and nothing else is a
	// configured websocket rather than a missing one.
	T.Run("WebSocket provider without a websocket block", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderWebSocket,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	// An unset provider used to validate clean and then be refused by both
	// constructors, which is the one config validation had nothing to say about.
	T.Run("unset provider", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&Config{}).ValidateWithContext(t.Context()))
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

func TestNewEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("SSE", func(t *testing.T) {
		t.Parallel()

		upgrader, err := NewEventStreamUpgrader(
			t.Context(),
			&Config{
				Provider: ProviderSSE,
			},
			nil,
		)

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("WebSocket", func(t *testing.T) {
		t.Parallel()

		upgrader, err := NewEventStreamUpgrader(
			t.Context(),
			&Config{
				Provider: ProviderWebSocket,
			},
			nil,
		)

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := NewEventStreamUpgrader(t.Context(), &Config{}, nil)

		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})
}

func TestNewBidirectionalEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("SSE returns error", func(t *testing.T) {
		t.Parallel()

		_, err := NewBidirectionalEventStreamUpgrader(
			t.Context(),
			&Config{
				Provider: ProviderSSE,
			},
			nil,
		)

		test.Error(t, err)
		test.StrContains(t, err.Error(), "SSE does not support bidirectional")
	})

	T.Run("WebSocket", func(t *testing.T) {
		t.Parallel()

		upgrader, err := NewBidirectionalEventStreamUpgrader(
			t.Context(),
			&Config{
				Provider: ProviderWebSocket,
			},
			nil,
		)

		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})

	T.Run("invalid provider", func(t *testing.T) {
		t.Parallel()

		_, err := NewBidirectionalEventStreamUpgrader(t.Context(), &Config{}, nil)

		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})
}

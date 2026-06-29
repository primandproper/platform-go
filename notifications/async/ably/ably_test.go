package ably

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/notifications/async"
	"github.com/primandproper/platform-go/v2/observability"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type mockChannelPublisher struct {
	publishFn func(ctx context.Context, channel, name string, data any) error
}

func (m *mockChannelPublisher) Publish(ctx context.Context, channel, name string, data any) error {
	return m.publishFn(ctx, channel, name, data)
}

// newRecordingNotifier builds a Notifier with a RecordingObserver swapped in, so a
// test can both drive Publish and assert which fields it observed.
func newRecordingNotifier(t *testing.T, publisher ChannelPublisher) (*Notifier, *observability.RecordingObserver) {
	t.Helper()

	mp := metricsnoop.NewMetricsProvider()
	sendCounter, _ := mp.NewInt64Counter("test_sends")
	errorCounter, _ := mp.NewInt64Counter("test_errors")

	obs := observability.NewRecordingObserver()

	n := &Notifier{
		o11y:         obs,
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
		publisher:    publisher,
	}

	return n, obs
}

func TestNewNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{
			APIKey: "appid.keyid:keysecret",
		}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		must.NoError(t, err)
		must.NotNil(t, n)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(nil, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil)
		test.Error(t, err)
		test.Nil(t, n)
	})
}

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var capturedChannel, capturedName string
		n, obs := newRecordingNotifier(t, &mockChannelPublisher{
			publishFn: func(_ context.Context, channel, name string, _ any) error {
				capturedChannel = channel
				capturedName = name
				return nil
			},
		})

		err := n.Publish(t.Context(), "my-channel", &async.Event{
			Type: "greeting",
			Data: json.RawMessage(`{"hello":"world"}`),
		})
		test.NoError(t, err)
		test.EqOp(t, "my-channel", capturedChannel)
		test.EqOp(t, "greeting", capturedName)

		obs.ObservedOperationWithData(t, map[string]any{
			"channel":    "my-channel",
			"event.type": "greeting",
		})
	})

	T.Run("publish error", func(t *testing.T) {
		t.Parallel()

		n, obs := newRecordingNotifier(t, &mockChannelPublisher{
			publishFn: func(context.Context, string, string, any) error {
				return errors.New("ably API error")
			},
		})

		err := n.Publish(t.Context(), "my-channel", &async.Event{
			Type: "test",
		})
		test.Error(t, err)

		// Even though the publish failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"channel":    "my-channel",
			"event.type": "test",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, _ := newRecordingNotifier(t, nil)

		test.NoError(t, n.Close())
	})
}

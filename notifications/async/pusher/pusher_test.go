package pusher

import (
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/notifications/async"
	"github.com/primandproper/platform-go/observability"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type mockPusherClient struct {
	triggerFn func(channel, eventName string, data any) error
}

func (m *mockPusherClient) Trigger(channel, eventName string, data any) error {
	return m.triggerFn(channel, eventName, data)
}

func TestNewNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{
			AppID:   "123",
			Key:     "key",
			Secret:  "secret",
			Cluster: "us2",
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

// newRecordingNotifier builds a Notifier with a RecordingObserver swapped in, so a
// test can both drive Publish and assert which fields it observed.
func newRecordingNotifier(t *testing.T, client PusherClient) (*Notifier, *observability.RecordingObserver) {
	t.Helper()

	mp := metricsnoop.NewMetricsProvider()
	sendCounter, _ := mp.NewInt64Counter("test_sends")
	errorCounter, _ := mp.NewInt64Counter("test_errors")

	obs := observability.NewRecordingObserver()
	n := &Notifier{
		o11y:         obs,
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
		client:       client,
	}

	return n, obs
}

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var capturedChannel, capturedEvent string
		n, obs := newRecordingNotifier(t, &mockPusherClient{
			triggerFn: func(channel, eventName string, data any) error {
				capturedChannel = channel
				capturedEvent = eventName
				return nil
			},
		})

		err := n.Publish(t.Context(), "my-channel", &async.Event{
			Type: "greeting",
			Data: json.RawMessage(`{"hello":"world"}`),
		})
		test.NoError(t, err)
		test.EqOp(t, "my-channel", capturedChannel)
		test.EqOp(t, "greeting", capturedEvent)

		obs.ObservedOperationWithData(t, map[string]any{
			"pusher.channel":    "my-channel",
			"pusher.event_type": "greeting",
		})
	})

	T.Run("trigger error", func(t *testing.T) {
		t.Parallel()

		n, obs := newRecordingNotifier(t, &mockPusherClient{
			triggerFn: func(string, string, any) error {
				return errors.New("pusher API error")
			},
		})

		err := n.Publish(t.Context(), "my-channel", &async.Event{
			Type: "test",
		})
		test.Error(t, err)

		// Even though the publish failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"pusher.channel":    "my-channel",
			"pusher.event_type": "test",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n := &Notifier{
			o11y: observability.NewRecordingObserver(),
		}

		test.NoError(t, n.Close())
	})
}

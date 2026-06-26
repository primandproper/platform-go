package pusher

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/notifications/async"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"
	"github.com/primandproper/platform-go/observability/tracing"
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

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		mp := metricsnoop.NewMetricsProvider()
		sendCounter, _ := mp.NewInt64Counter("test_sends")
		errorCounter, _ := mp.NewInt64Counter("test_errors")

		var capturedChannel, capturedEvent string
		n := &Notifier{
			logger:       loggingnoop.NewLogger(),
			tracer:       tracing.NewTracerForTest("test"),
			sendCounter:  sendCounter,
			errorCounter: errorCounter,
			client: &mockPusherClient{
				triggerFn: func(channel, eventName string, data any) error {
					capturedChannel = channel
					capturedEvent = eventName
					return nil
				},
			},
		}

		err := n.Publish(context.Background(), "my-channel", &async.Event{
			Type: "greeting",
			Data: json.RawMessage(`{"hello":"world"}`),
		})
		test.NoError(t, err)
		test.EqOp(t, "my-channel", capturedChannel)
		test.EqOp(t, "greeting", capturedEvent)
	})

	T.Run("trigger error", func(t *testing.T) {
		t.Parallel()

		mp := metricsnoop.NewMetricsProvider()
		sendCounter, _ := mp.NewInt64Counter("test_sends")
		errorCounter, _ := mp.NewInt64Counter("test_errors")

		n := &Notifier{
			logger:       loggingnoop.NewLogger(),
			tracer:       tracing.NewTracerForTest("test"),
			sendCounter:  sendCounter,
			errorCounter: errorCounter,
			client: &mockPusherClient{
				triggerFn: func(string, string, any) error {
					return errors.New("pusher API error")
				},
			},
		}

		err := n.Publish(context.Background(), "my-channel", &async.Event{
			Type: "test",
		})
		test.Error(t, err)
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n := &Notifier{
			logger: loggingnoop.NewLogger(),
			tracer: tracing.NewTracerForTest("test"),
		}

		test.NoError(t, n.Close())
	})
}

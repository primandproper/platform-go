package ably

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

type mockChannelPublisher struct {
	publishFn func(ctx context.Context, channel, name string, data any) error
}

func (m *mockChannelPublisher) Publish(ctx context.Context, channel, name string, data any) error {
	return m.publishFn(ctx, channel, name, data)
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

		mp := metricsnoop.NewMetricsProvider()
		sendCounter, _ := mp.NewInt64Counter("test_sends")
		errorCounter, _ := mp.NewInt64Counter("test_errors")

		var capturedChannel, capturedName string
		n := &Notifier{
			logger:       loggingnoop.NewLogger(),
			tracer:       tracing.NewTracerForTest("test"),
			sendCounter:  sendCounter,
			errorCounter: errorCounter,
			publisher: &mockChannelPublisher{
				publishFn: func(_ context.Context, channel, name string, _ any) error {
					capturedChannel = channel
					capturedName = name
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
		test.EqOp(t, "greeting", capturedName)
	})

	T.Run("publish error", func(t *testing.T) {
		t.Parallel()

		mp := metricsnoop.NewMetricsProvider()
		sendCounter, _ := mp.NewInt64Counter("test_sends")
		errorCounter, _ := mp.NewInt64Counter("test_errors")

		n := &Notifier{
			logger:       loggingnoop.NewLogger(),
			tracer:       tracing.NewTracerForTest("test"),
			sendCounter:  sendCounter,
			errorCounter: errorCounter,
			publisher: &mockChannelPublisher{
				publishFn: func(context.Context, string, string, any) error {
					return errors.New("ably API error")
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

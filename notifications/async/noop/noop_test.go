package noop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v2/notifications/async"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestAsyncNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewAsyncNotifier()
		must.NoError(t, err)
		must.NotNil(t, n)

		err = n.Publish(context.Background(), "test-channel", &async.Event{
			Type: "test",
			Data: json.RawMessage(`{"key":"value"}`),
		})
		test.NoError(t, err)
	})
}

func TestAsyncNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewAsyncNotifier()
		must.NoError(t, err)
		must.NotNil(t, n)

		test.NoError(t, n.Close())
	})
}

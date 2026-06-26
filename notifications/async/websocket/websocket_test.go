package websocket

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/notifications/async"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)
		must.NotNil(t, n)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(nil, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		test.Error(t, err)
		test.Nil(t, n)
	})
}

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("no connected clients", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		err = n.Publish(context.Background(), "test-channel", &async.Event{
			Type: "test",
			Data: json.RawMessage(`{"key":"value"}`),
		})
		test.NoError(t, err)
	})

	T.Run("with connected client", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acceptErr := n.AcceptConnection(w, r, "test-channel", "member-1")
			test.NoError(t, acceptErr)
			// keep the handler alive so the websocket stays open
			<-r.Context().Done()
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		conn, _, err := gorillawebsocket.DefaultDialer.Dial(wsURL, nil)
		must.NoError(t, err)
		defer conn.Close()

		// give the connection time to register
		time.Sleep(50 * time.Millisecond)

		err = n.Publish(context.Background(), "test-channel", &async.Event{
			Type: "greeting",
			Data: json.RawMessage(`{"hello":"world"}`),
		})
		test.NoError(t, err)

		var received map[string]json.RawMessage
		err = conn.ReadJSON(&received)
		must.NoError(t, err)
		test.Eq(t, json.RawMessage(`"greeting"`), received["type"])
	})
}

func TestNotifier_AcceptConnection(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acceptErr := n.AcceptConnection(w, r, "channel", "member")
			test.NoError(t, acceptErr)
			<-r.Context().Done()
		}))
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
		conn, _, err := gorillawebsocket.DefaultDialer.Dial(wsURL, nil)
		must.NoError(t, err)
		defer conn.Close()
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
		must.NoError(t, err)

		test.NoError(t, n.Close())
	})
}

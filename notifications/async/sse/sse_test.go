package sse

import (
	"bufio"
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
		must.NoError(t, err)
		must.NotNil(t, n)
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

		ready := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(ready)
			acceptErr := n.AcceptConnection(w, r, "test-channel", "member-1")
			test.NoError(t, acceptErr)
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)

		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		<-ready
		// give the connection time to register
		time.Sleep(50 * time.Millisecond)

		err = n.Publish(context.Background(), "test-channel", &async.Event{
			Type: "greeting",
			Data: json.RawMessage(`{"hello":"world"}`),
		})
		test.NoError(t, err)

		scanner := bufio.NewScanner(resp.Body)
		var eventLine, dataLine string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event:") {
				eventLine = line
			}
			if strings.HasPrefix(line, "data:") {
				dataLine = line
				break
			}
		}

		test.StrContains(t, eventLine, "greeting")
		test.StrContains(t, dataLine, `{"hello":"world"}`)
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

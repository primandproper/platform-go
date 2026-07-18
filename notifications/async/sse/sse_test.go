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

	"github.com/primandproper/platform-go/v5/notifications/async"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingNotifier builds a Notifier with a RecordingObserver swapped in, so a
// test can both drive a method and assert which operations it observed.
func newRecordingNotifier(t *testing.T) (*Notifier, *observability.RecordingObserver) {
	t.Helper()

	n, err := NewNotifier(&Config{}, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider())
	must.NoError(t, err)
	must.NotNil(t, n)

	obs := observability.NewRecordingObserver()
	n.o11y = obs

	return n, obs
}

// nonFlushingResponseWriter is an http.ResponseWriter that deliberately does not
// implement http.Flusher, so the SSE upgrade fails.
type nonFlushingResponseWriter struct {
	header http.Header
}

func (w *nonFlushingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}
	return w.header
}

func (w *nonFlushingResponseWriter) Write(b []byte) (int, error) { return len(b), nil }

func (w *nonFlushingResponseWriter) WriteHeader(int) {}

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

		n, obs := newRecordingNotifier(t)

		event := &async.Event{
			Type: "test",
			Data: json.RawMessage(`{"key":"value"}`),
		}

		err := n.Publish(context.Background(), "test-channel", event)
		test.NoError(t, err)

		// Publish observes the channel, event type, and payload length.
		must.SliceLen(t, 1, obs.Operations)
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey:  "test-channel",
			"event.type":   event.Type,
			keys.LengthKey: len(event.Data),
		})
		test.True(t, op.Ended)
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

func TestNotifier_AcceptConnection(T *testing.T) {
	T.Parallel()

	T.Run("with failing upgrade", func(t *testing.T) {
		t.Parallel()

		n, obs := newRecordingNotifier(t)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com", http.NoBody)
		must.NoError(t, err)

		// A ResponseWriter that does not implement http.Flusher fails the upgrade.
		err = n.AcceptConnection(&nonFlushingResponseWriter{}, req, "test-channel", "member-1")
		must.Error(t, err)

		// The channel and member are observed before the upgrade is attempted,
		// and the failure must still be recorded on an ended operation.
		must.SliceLen(t, 1, obs.Operations)
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey: "test-channel",
			"member_id":   "member-1",
		})
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
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

package websocket

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/notifications/async"

	"github.com/primandproper/primitives-go/v2/eventstream"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/keys"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingNotifier builds a Notifier with a RecordingObserver swapped in, so a
// test can both drive the notifier and assert which operations it observed.
func newRecordingNotifier(t *testing.T) (*Notifier, *observability.RecordingObserver) {
	t.Helper()

	n, err := NewNotifier(&Config{})
	must.NoError(t, err)
	must.NotNil(t, n)

	obs := observability.NewRecordingObserver()
	n.o11y = obs

	return n, obs
}

var errStub = errors.New("stub error")

// failingStream is an eventstream.EventStream whose Send always fails, standing
// in for a client the notifier can no longer reach.
type failingStream struct{}

func (*failingStream) Send(context.Context, *eventstream.Event) error { return errStub }
func (*failingStream) Done() <-chan struct{}                          { return make(chan struct{}) }
func (*failingStream) Close() error                                   { return nil }

func TestNewNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{})
		must.NoError(t, err)
		must.NotNil(t, n)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(nil)
		test.Error(t, err)
		test.Nil(t, n)
	})
}

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("propagates a client's send failure", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		n, obs := newRecordingNotifier(t)
		n.manager.Add(ctx, "test-channel", "member-1", &failingStream{})

		event := &async.Event{
			Type: "test",
			Data: json.RawMessage(`{"key":"value"}`),
		}

		err := n.Publish(ctx, "test-channel", event)

		// A publish that reached nobody is no longer reported as a success: the
		// broadcast's joined per-client failures come back from Publish.
		must.ErrorIs(t, err, errStub)

		must.SliceLen(t, 1, obs.Operations)
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.ChannelKey:   "test-channel",
			keys.EventTypeKey: event.Type,
			keys.LengthKey:    len(event.Data),
		})
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("no connected clients", func(t *testing.T) {
		t.Parallel()

		n, obs := newRecordingNotifier(t)

		err := n.Publish(context.Background(), "test-channel", &async.Event{
			Type: "test",
			Data: json.RawMessage(`{"key":"value"}`),
		})
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{})
	})

	T.Run("with connected client", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{})
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

		n, err := NewNotifier(&Config{})
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

	T.Run("with upgrade failure", func(t *testing.T) {
		t.Parallel()

		n, obs := newRecordingNotifier(t)

		// A plain (non-websocket) request cannot be upgraded, so AcceptConnection
		// returns an error and records it on the operation.
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)

		err := n.AcceptConnection(rec, req, "channel", "member")
		test.Error(t, err)

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		n, err := NewNotifier(&Config{})
		must.NoError(t, err)

		test.NoError(t, n.Close())
	})
}

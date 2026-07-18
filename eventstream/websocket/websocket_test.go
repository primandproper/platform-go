package websocket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/eventstream"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	gorillawebsocket "github.com/gorilla/websocket"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

func TestNewUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("nil config uses defaults", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), nil)
		must.NotNil(t, u)
		test.EqOp(t, defaultHeartbeatInterval, u.heartbeatInterval)
		test.EqOp(t, defaultBufferSize, u.wsUpgrader.ReadBufferSize)
		test.EqOp(t, defaultBufferSize, u.wsUpgrader.WriteBufferSize)
	})

	T.Run("custom config", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			HeartbeatInterval: 10 * time.Second,
			ReadBufferSize:    2048,
			WriteBufferSize:   4096,
		})
		must.NotNil(t, u)
		test.EqOp(t, 10*time.Second, u.heartbeatInterval)
		test.EqOp(t, 2048, u.wsUpgrader.ReadBufferSize)
		test.EqOp(t, 4096, u.wsUpgrader.WriteBufferSize)
	})
}

func TestUpgrader_UpgradeToEventStream(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()
	})
}

func TestUpgrader_UpgradeToBidirectionalStream(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		test.NotNil(t, stream.Receive())
	})
}

func TestUpgrader_UpgradeToEventStream_upgradeError(T *testing.T) {
	T.Parallel()

	T.Run("non-websocket request returns error", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), nil)
		errCh := make(chan error, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, upgradeErr := u.UpgradeToEventStream(w, r)
			errCh <- upgradeErr
		}))
		defer server.Close()

		// A plain HTTP GET lacks the WebSocket upgrade headers, so the handshake fails.
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		gotErr := <-errCh
		test.Error(t, gotErr)
		test.StrContains(t, gotErr.Error(), "upgrading to websocket")
	})
}

func TestUpgrader_UpgradeToBidirectionalStream_upgradeError(T *testing.T) {
	T.Parallel()

	T.Run("non-websocket request returns error", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), nil)
		errCh := make(chan error, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, upgradeErr := u.UpgradeToBidirectionalStream(w, r)
			errCh <- upgradeErr
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		gotErr := <-errCh
		test.Error(t, gotErr)
		test.StrContains(t, gotErr.Error(), "upgrading to websocket")
	})
}

func TestWSStream_heartbeatLoop_writeError(T *testing.T) {
	T.Parallel()

	T.Run("logs and stops when the ping write fails", func(t *testing.T) {
		t.Parallel()

		spy := newSpyLogger()
		conn := rawServerConn(t)
		o11y := observability.NewObserver(name, spy, tracingnoop.NewTracerProvider())

		// Construct the stream directly and drive only the heartbeat loop, so the
		// read pump doesn't race it to detect the broken connection first.
		stream := &wsStream{
			conn:              conn,
			done:              make(chan struct{}),
			o11y:              o11y,
			heartbeatInterval: time.NewTicker(time.Millisecond),
		}
		go stream.heartbeatLoop()
		t.Cleanup(func() { _ = stream.Close() })

		// Break the underlying connection so the next heartbeat ping write fails.
		must.NoError(t, conn.Close())

		select {
		case <-spy.errored:
			// expected: the heartbeat write error was logged and the loop stopped.
		case <-time.After(2 * time.Second):
			t.Fatalf("heartbeat write error was never logged")
		}
	})
}

func TestBidirectionalWSStream_readLoop_malformedMessage(T *testing.T) {
	T.Parallel()

	T.Run("skips unparseable messages and continues", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		defer stream.Close()

		// First message is not valid JSON and must be skipped.
		must.NoError(t, conn.WriteMessage(gorillawebsocket.TextMessage, []byte("this is not json")))
		// Second message is valid; receiving it proves the read loop continued past the bad one.
		must.NoError(t, conn.WriteJSON(&eventstream.Event{Type: "good", Payload: json.RawMessage(`{"ok":true}`)}))

		select {
		case event := <-stream.Receive():
			must.NotNil(t, event)
			test.EqOp(t, "good", event.Type)
		case <-time.After(2 * time.Second):
			t.Fatalf("did not receive the valid event after a malformed one")
		}
	})
}

func TestBidirectionalWSStream_readLoop_doneWhileBuffered(T *testing.T) {
	T.Parallel()

	T.Run("returns when closed while the incoming buffer is full", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		incoming := stream.Receive()

		// Flood without reading so the incoming buffer fills and readLoop parks in the
		// select trying to enqueue the next event.
		for range incomingChannelBuffer * 2 {
			must.NoError(t, conn.WriteJSON(&eventstream.Event{Type: "flood"}))
		}

		// Let readLoop fill the buffer and block on the pending send.
		time.Sleep(200 * time.Millisecond)

		// With the buffer full, the only ready case after Close is <-s.done, so readLoop
		// takes the done path and returns.
		must.NoError(t, stream.Close())
		time.Sleep(50 * time.Millisecond)

		// Draining must eventually observe the closed channel, proving readLoop returned.
		timeout := time.After(2 * time.Second)
		for {
			select {
			case _, open := <-incoming:
				if !open {
					return
				}
			case <-timeout:
				t.Fatalf("incoming channel was not closed after stream.Close()")
			}
		}
	})
}

func TestWSStream_Send(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		received := make(chan *eventstream.Event, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			defer stream.Close()

			_ = stream.Send(r.Context(), &eventstream.Event{
				Type:    "test",
				Payload: json.RawMessage(`{"msg":"hello"}`),
			})
			// keep alive briefly so client can read
			time.Sleep(100 * time.Millisecond)
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		go func() {
			var event eventstream.Event
			if readErr := conn.ReadJSON(&event); readErr == nil {
				received <- &event
			}
		}()

		select {
		case event := <-received:
			test.EqOp(t, "test", event.Type)
			test.EqOp(t, `{"msg":"hello"}`, string(event.Payload))
		case <-time.After(2 * time.Second):
			t.Fatalf("did not receive event")
		}
	})

	T.Run("send after close returns error", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		must.NoError(t, stream.Close())

		sendErr := stream.Send(t.Context(), &eventstream.Event{Type: "x"})
		test.Error(t, sendErr)
		test.StrContains(t, sendErr.Error(), "stream closed")
	})

	T.Run("observes the send operation", func(t *testing.T) {
		t.Parallel()

		obs := observability.NewRecordingObserver()
		conn := rawServerConn(t)
		stream := newWSStream(conn, 0, obs)
		t.Cleanup(func() { _ = stream.Close() })

		must.NoError(t, stream.Send(t.Context(), &eventstream.Event{
			Type:    "test",
			Payload: json.RawMessage(`{"msg":"hello"}`),
		}))

		// Send attaches the event type to its operation before writing.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"event.type": "test",
		})
		test.True(t, op.Ended)
	})
}

func TestWSStream_Done(T *testing.T) {
	T.Parallel()

	T.Run("closes on Close", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		done := stream.Done()
		must.NoError(t, stream.Close())

		select {
		case <-done:
			// expected
		case <-time.After(time.Second):
			t.Fatalf("Done() channel was not closed after Close()")
		}
	})
}

func TestWSStream_Done_clientDisconnect(T *testing.T) {
	T.Parallel()

	T.Run("closes when the client disconnects", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)

		stream := <-streamReady
		done := stream.Done()

		// The client goes away; the read pump must notice and terminate the stream
		// even though no one ever called the server-side Close.
		must.NoError(t, conn.Close())

		select {
		case <-done:
			// expected: read pump detected the disconnect and closed the stream.
		case <-time.After(2 * time.Second):
			t.Fatalf("Done() channel was not closed after the client disconnected")
		}
	})
}

func TestBidirectionalWSStream_Done_clientDisconnect(T *testing.T) {
	T.Parallel()

	T.Run("closes when the client disconnects", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)

		stream := <-streamReady
		done := stream.Done()

		must.NoError(t, conn.Close())

		select {
		case <-done:
			// expected
		case <-time.After(2 * time.Second):
			t.Fatalf("Done() channel was not closed after the client disconnected")
		}
	})
}

func TestUpgrader_CheckOrigin(T *testing.T) {
	T.Parallel()

	T.Run("same-origin is allowed by default", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			defer stream.Close()
			<-stream.Done()
		}))
		defer server.Close()

		// server.URL's host matches the request host, so this is same-origin.
		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()
	})

	T.Run("cross-origin is rejected by default", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = u.UpgradeToEventStream(w, r)
		}))
		defer server.Close()

		_, resp, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {"http://evil.example.com"}})
		test.ErrorIs(t, err, gorillawebsocket.ErrBadHandshake)
		must.NotNil(t, resp)
		test.EqOp(t, http.StatusForbidden, resp.StatusCode)
	})

	T.Run("configured allowlist permits listed origins and rejects others", func(t *testing.T) {
		t.Parallel()

		const allowed = "http://allowed.example.com"

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{
			HeartbeatInterval: time.Hour,
			AllowedOrigins:    []string{allowed},
		})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			defer stream.Close()
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {allowed}})
		must.NoError(t, err)
		defer conn.Close()

		// An origin outside the allowlist (including the otherwise same-origin host) is rejected.
		_, resp, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		test.ErrorIs(t, err, gorillawebsocket.ErrBadHandshake)
		must.NotNil(t, resp)
		test.EqOp(t, http.StatusForbidden, resp.StatusCode)
	})
}

func TestWSStream_Close(T *testing.T) {
	T.Parallel()

	T.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		test.NoError(t, stream.Close())
		test.NoError(t, stream.Close())
	})
}

func TestBidirectionalWSStream_Receive(T *testing.T) {
	T.Parallel()

	T.Run("receives client messages", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		defer stream.Close()

		// Client sends an event
		outgoing := &eventstream.Event{
			Type:    "ping",
			Payload: json.RawMessage(`{"seq":1}`),
		}
		must.NoError(t, conn.WriteJSON(outgoing))

		select {
		case event := <-stream.Receive():
			must.NotNil(t, event)
			test.EqOp(t, "ping", event.Type)
			test.EqOp(t, `{"seq":1}`, string(event.Payload))
		case <-time.After(2 * time.Second):
			t.Fatalf("did not receive event from client")
		}
	})

	T.Run("channel closes when stream is closed", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(nil, tracingnoop.NewTracerProvider(), &Config{HeartbeatInterval: time.Hour})
		streamReady := make(chan eventstream.BidirectionalEventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stream, err := u.UpgradeToBidirectionalStream(w, r)
			if err != nil {
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		conn, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
		must.NoError(t, err)
		defer conn.Close()

		stream := <-streamReady
		incoming := stream.Receive()

		must.NoError(t, stream.Close())

		select {
		case _, open := <-incoming:
			test.False(t, open, test.Sprintf("Receive channel should be closed"))
		case <-time.After(2 * time.Second):
			t.Fatalf("Receive channel was not closed after stream.Close()")
		}
	})
}

// rawServerConn dials a throwaway httptest WebSocket server and returns the
// server-side gorilla connection, so a stream can be constructed directly and
// its connection broken from the test.
func rawServerConn(t *testing.T) *gorillawebsocket.Conn {
	t.Helper()

	connCh := make(chan *gorillawebsocket.Conn, 1)
	upgrader := gorillawebsocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		connCh <- c
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	client, _, err := gorillawebsocket.DefaultDialer.Dial("ws"+server.URL[4:], http.Header{"Origin": {server.URL}})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	return <-connCh
}

// spyLogger is a logging.Logger that signals when Error is called.
type spyLogger struct {
	errored chan struct{}
}

func newSpyLogger() *spyLogger {
	return &spyLogger{errored: make(chan struct{}, 1)}
}

func (l *spyLogger) Error(string, error) {
	select {
	case l.errored <- struct{}{}:
	default:
	}
}

func (l *spyLogger) Info(string)                                {}
func (l *spyLogger) Debug(string)                               {}
func (l *spyLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}
func (l *spyLogger) Clone() logging.Logger                      { return l }
func (l *spyLogger) WithName(string) logging.Logger             { return l }
func (l *spyLogger) WithValues(map[string]any) logging.Logger   { return l }
func (l *spyLogger) WithValue(string, any) logging.Logger       { return l }
func (l *spyLogger) WithRequest(*http.Request) logging.Logger   { return l }
func (l *spyLogger) WithResponse(*http.Response) logging.Logger { return l }
func (l *spyLogger) WithError(error) logging.Logger             { return l }
func (l *spyLogger) WithSpan(trace.Span) logging.Logger         { return l }

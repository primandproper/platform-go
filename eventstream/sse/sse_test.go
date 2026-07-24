package sse

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v6/eventstream"
	"github.com/primandproper/platform-go/v6/observability"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		u := NewUpgrader(tracingnoop.NewTracerProvider())
		test.NotNil(t, u)
	})
}

func TestUpgrader_UpgradeToEventStream(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		test.EqOp(t, "text/event-stream", resp.Header.Get("Content-Type"))
		test.EqOp(t, "no-cache", resp.Header.Get("Cache-Control"))
		test.EqOp(t, "keep-alive", resp.Header.Get("Connection"))
	})

	T.Run("response writer does not support flushing", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		u := NewUpgrader(tracingnoop.NewTracerProvider())
		w := &nonFlushableResponseWriter{header: http.Header{}}
		r := httptest.NewRequestWithContext(ctx, http.MethodGet, "/", http.NoBody)

		stream, err := u.UpgradeToEventStream(w, r)
		test.Nil(t, stream)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "streaming not supported")
	})
}

func TestSSEStream_Send(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		sendErr := stream.Send(t.Context(), &eventstream.Event{
			Type:    "test_event",
			Payload: json.RawMessage(`{"msg":"hello"}`),
		})
		must.NoError(t, sendErr)

		scanner := bufio.NewScanner(resp.Body)

		// Read "event: test_event"
		must.True(t, scanner.Scan())
		test.EqOp(t, "event: test_event", scanner.Text())

		// Read "data: {\"msg\":\"hello\"}"
		must.True(t, scanner.Scan())
		test.EqOp(t, `data: {"msg":"hello"}`, scanner.Text())

		// Read empty line (event separator)
		must.True(t, scanner.Scan())
		test.EqOp(t, "", scanner.Text())
	})

	T.Run("neutralizes newlines in the payload to prevent SSE injection", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		// A payload embedding a newline followed by a would-be control field must be
		// emitted as multiple "data:" lines, not injected as a separate "event:" field.
		sendErr := stream.Send(t.Context(), &eventstream.Event{
			Payload: json.RawMessage("line1\nevent: injected"),
		})
		must.NoError(t, sendErr)

		scanner := bufio.NewScanner(resp.Body)

		must.True(t, scanner.Scan())
		test.EqOp(t, "data: line1", scanner.Text())

		must.True(t, scanner.Scan())
		test.EqOp(t, "data: event: injected", scanner.Text())

		must.True(t, scanner.Scan())
		test.EqOp(t, "", scanner.Text())
	})

	T.Run("event without type", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		sendErr := stream.Send(t.Context(), &eventstream.Event{
			Payload: json.RawMessage(`{"x":1}`),
		})
		must.NoError(t, sendErr)

		scanner := bufio.NewScanner(resp.Body)

		// No "event:" line, just data
		must.True(t, scanner.Scan())
		test.EqOp(t, `data: {"x":1}`, scanner.Text())

		// Empty line (event separator)
		must.True(t, scanner.Scan())
		test.EqOp(t, "", scanner.Text())
	})

	T.Run("multiple events", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		for i, name := range []string{"first", "second", "third"} {
			sendErr := stream.Send(t.Context(), &eventstream.Event{
				Type:    "msg",
				Payload: json.RawMessage(`"` + name + `"`),
			})
			must.NoError(t, sendErr, must.Sprintf("send %d", i))
		}

		scanner := bufio.NewScanner(resp.Body)
		for _, name := range []string{"first", "second", "third"} {
			must.True(t, scanner.Scan())
			test.EqOp(t, "event: msg", scanner.Text())

			must.True(t, scanner.Scan())
			test.EqOp(t, `data: "`+name+`"`, scanner.Text())

			must.True(t, scanner.Scan())
			test.EqOp(t, "", scanner.Text())
		}
	})

	T.Run("send after close returns error", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)

		must.NoError(t, stream.Close())

		sendErr := stream.Send(t.Context(), &eventstream.Event{
			Type:    "test",
			Payload: json.RawMessage(`{}`),
		})
		test.Error(t, sendErr)
		test.StrContains(t, sendErr.Error(), "stream closed")
	})
}

func TestSSEStream_Done(T *testing.T) {
	T.Parallel()

	T.Run("closes on Close", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)

		done := stream.Done()
		must.NoError(t, stream.Close())

		select {
		case <-done:
			// expected: channel closed
		case <-time.After(time.Second):
			t.Fatalf("Done() channel was not closed after Close()")
		}
	})

	T.Run("closes on client disconnect", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)

		stream := <-streamReady
		must.NotNil(t, stream)

		// Close the client connection, which cancels the request context
		resp.Body.Close()

		// The done channel should close because the request context was cancelled
		select {
		case <-stream.Done():
			// expected
		case <-time.After(2 * time.Second):
			t.Fatalf("Done() channel was not closed after client disconnect")
		}
	})
}

func TestSSEStream_Close(T *testing.T) {
	T.Parallel()

	T.Run("idempotent", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)

		// Close should be idempotent (context.CancelFunc is safe to call multiple times)
		test.NoError(t, stream.Close())
		test.NoError(t, stream.Close())
	})
}

// nonFlushableResponseWriter is a minimal ResponseWriter that does NOT implement http.Flusher.
type nonFlushableResponseWriter struct {
	header http.Header
}

func (w *nonFlushableResponseWriter) Header() http.Header         { return w.header }
func (w *nonFlushableResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *nonFlushableResponseWriter) WriteHeader(int)             {}

// failingResponseWriter is a flushable ResponseWriter whose Write always fails.
type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header       { return w.header }
func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, errWriteFailed }
func (w *failingResponseWriter) WriteHeader(int)           {}
func (w *failingResponseWriter) Flush()                    {}

var errWriteFailed = errors.New("write failed")

// newFailingStream builds an sseStream whose writes always fail, with a
// RecordingObserver swapped in so a test can assert the failure was observed on
// the operation.
func newFailingStream() (*sseStream, *observability.RecordingObserver) {
	w := &failingResponseWriter{header: http.Header{}}
	obs := observability.NewRecordingObserver()
	return &sseStream{
		w:       w,
		flusher: w,
		done:    make(chan struct{}),
		o11y:    obs,
	}, obs
}

func TestSSEStream_Send_writeErrors(T *testing.T) {
	T.Parallel()

	T.Run("error writing event type", func(t *testing.T) {
		t.Parallel()

		s, obs := newFailingStream()

		err := s.Send(t.Context(), &eventstream.Event{
			Type:    "boom",
			Payload: json.RawMessage(`{}`),
		})
		test.Error(t, err)
		test.StrContains(t, err.Error(), "writing event type")

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("error writing event data", func(t *testing.T) {
		t.Parallel()

		s, obs := newFailingStream()

		// Empty Type skips the type write and reaches the data write directly.
		err := s.Send(t.Context(), &eventstream.Event{
			Payload: json.RawMessage(`{}`),
		})
		test.Error(t, err)
		test.StrContains(t, err.Error(), "writing event data")

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestSSEStream_Send_verifies_SSE_format(T *testing.T) {
	T.Parallel()

	T.Run("output is valid SSE", func(t *testing.T) {
		t.Parallel()

		streamReady := make(chan eventstream.EventStream, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := NewUpgrader(tracingnoop.NewTracerProvider())
			stream, err := u.UpgradeToEventStream(w, r)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			streamReady <- stream
			<-stream.Done()
		}))
		defer server.Close()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, http.NoBody)
		must.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		must.NoError(t, err)
		defer resp.Body.Close()

		stream := <-streamReady
		must.NotNil(t, stream)
		defer stream.Close()

		sendErr := stream.Send(t.Context(), &eventstream.Event{
			Type:    "update",
			Payload: json.RawMessage(`{"id":"abc","status":"done"}`),
		})
		must.NoError(t, sendErr)

		// Read raw bytes and verify the exact SSE format
		buf := make([]byte, 4096)
		n, readErr := resp.Body.Read(buf)
		must.NoError(t, readErr)

		output := string(buf[:n])
		expected := "event: update\ndata: {\"id\":\"abc\",\"status\":\"done\"}\n\n"
		test.EqOp(t, expected, output)
	})
}

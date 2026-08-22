package http

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// plainWriter implements only http.ResponseWriter, so a test can assert the
// recorder does not invent capabilities.
type plainWriter struct {
	header http.Header
	body   strings.Builder
	status int
}

func newPlainWriter() *plainWriter {
	return &plainWriter{header: http.Header{}}
}

func (w *plainWriter) Header() http.Header         { return w.header }
func (w *plainWriter) WriteHeader(status int)      { w.status = status }
func (w *plainWriter) Write(b []byte) (int, error) { return w.body.Write(b) }

type flusherWriter struct {
	*plainWriter
	flushed bool
}

func (w *flusherWriter) Flush() { w.flushed = true }

type hijackerWriter struct {
	*plainWriter
}

func (w *hijackerWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

type bothWriter struct {
	*plainWriter
	flushed bool
}

func (w *bothWriter) Flush()                                       { w.flushed = true }
func (w *bothWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) { return nil, nil, nil }

// shortWriter accepts only limit bytes per Write, standing in for a client
// that went away mid-response.
type shortWriter struct {
	*plainWriter
	limit int
}

func (w *shortWriter) Write(b []byte) (int, error) {
	if len(b) > w.limit {
		b = b[:w.limit]
	}

	return w.plainWriter.Write(b)
}

func TestRecorder(T *testing.T) {
	T.Parallel()

	T.Run("passes bytes through while recording them", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, rec := newRecorder(base, 1024)

		wrapped.WriteHeader(http.StatusCreated)
		_, err := wrapped.Write([]byte("hello "))
		must.NoError(t, err)
		_, err = wrapped.Write([]byte("world"))
		must.NoError(t, err)

		test.EqOp(t, http.StatusCreated, base.status)
		test.EqOp(t, "hello world", base.body.String())

		got := rec.response(nil)
		test.EqOp(t, http.StatusCreated, got.StatusCode)
		test.EqOp(t, "hello world", string(got.Body))
		test.False(t, got.Truncated)
	})

	T.Run("a handler that never writes records 200", func(t *testing.T) {
		t.Parallel()

		_, rec := newRecorder(newPlainWriter(), 1024)

		// net/http sends 200 for a handler that returns without writing, so
		// that is what a replay has to reproduce.
		got := rec.response(nil)
		test.EqOp(t, http.StatusOK, got.StatusCode)
		test.SliceEmpty(t, got.Body)
	})

	T.Run("an implicit write records 200", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 1024)

		_, err := wrapped.Write([]byte("body"))
		must.NoError(t, err)

		test.EqOp(t, http.StatusOK, rec.response(nil).StatusCode)
	})

	T.Run("ignores a second WriteHeader", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, rec := newRecorder(base, 1024)

		wrapped.WriteHeader(http.StatusCreated)
		wrapped.WriteHeader(http.StatusTeapot)

		test.EqOp(t, http.StatusCreated, rec.response(nil).StatusCode)
		test.EqOp(t, http.StatusCreated, base.status)
	})

	T.Run("records a body exactly at the cap", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, rec := newRecorder(base, 4)

		_, err := wrapped.Write([]byte("abcd"))
		must.NoError(t, err)

		got := rec.response(nil)
		test.False(t, got.Truncated)
		test.EqOp(t, "abcd", string(got.Body))
	})

	// Over the cap the status still records, so the effect does not repeat;
	// only the body is lost.
	T.Run("drops the body one byte over the cap", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, rec := newRecorder(base, 4)

		wrapped.WriteHeader(http.StatusCreated)
		_, err := wrapped.Write([]byte("abcde"))
		must.NoError(t, err)

		got := rec.response(nil)
		test.True(t, got.Truncated)
		test.SliceEmpty(t, got.Body)
		test.EqOp(t, http.StatusCreated, got.StatusCode)

		// The client still received everything.
		test.EqOp(t, "abcde", base.body.String())
	})

	T.Run("truncates across several writes", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 4)

		for range 3 {
			_, err := wrapped.Write([]byte("ab"))
			must.NoError(t, err)
		}

		test.True(t, rec.response(nil).Truncated)
	})

	T.Run("a zero cap records without limit", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 0)

		_, err := wrapped.Write([]byte(strings.Repeat("a", 4096)))
		must.NoError(t, err)

		got := rec.response(nil)
		test.False(t, got.Truncated)
		test.EqOp(t, 4096, len(got.Body))
	})

	// A recorded prefix of a response the client never fully received must not
	// be replayed as though it were complete.
	T.Run("records no body after a short write", func(t *testing.T) {
		t.Parallel()

		base := &shortWriter{plainWriter: newPlainWriter(), limit: 2}
		wrapped, rec := newRecorder(base, 1024)

		n, err := wrapped.Write([]byte("abcdef"))
		must.NoError(t, err)
		test.EqOp(t, 2, n)

		got := rec.response(nil)
		test.True(t, got.Truncated)
		test.SliceEmpty(t, got.Body)
	})

	T.Run("snapshots headers at WriteHeader", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, rec := newRecorder(base, 1024)

		wrapped.Header().Set("Content-Type", "application/json")
		wrapped.WriteHeader(http.StatusOK)

		// Anything set afterwards never reached the client, so it must not
		// reach the record either.
		wrapped.Header().Set("Content-Type", "text/plain")
		wrapped.Header().Set("X-Late", "yes")

		got := rec.response([]string{"Content-Type", "X-Late"})
		test.EqOp(t, "application/json", got.Header.Get("Content-Type"))
		test.EqOp(t, "", got.Header.Get("X-Late"))
	})

	T.Run("records only allowlisted headers", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 1024)

		wrapped.Header().Set("Content-Type", "application/json")
		wrapped.Header().Set("Set-Cookie", "session=abc")
		wrapped.Header().Set("X-Custom", "v")
		wrapped.WriteHeader(http.StatusOK)

		got := rec.response(defaultReplayedHeaders)
		test.EqOp(t, "application/json", got.Header.Get("Content-Type"))
		test.EqOp(t, "", got.Header.Get("Set-Cookie"))
		test.EqOp(t, "", got.Header.Get("X-Custom"))
	})

	T.Run("keeps repeated header values", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 1024)

		wrapped.Header().Add("X-Multi", "one")
		wrapped.Header().Add("X-Multi", "two")
		wrapped.WriteHeader(http.StatusOK)

		test.SliceLen(t, 2, rec.response([]string{"X-Multi"}).Header.Values("X-Multi"))
	})

	T.Run("Unwrap exposes the base writer", func(t *testing.T) {
		t.Parallel()

		base := newPlainWriter()
		wrapped, _ := newRecorder(base, 1024)

		unwrapper, ok := wrapped.(interface{ Unwrap() http.ResponseWriter })
		must.True(t, ok)
		test.EqOp(t, http.ResponseWriter(base), unwrapper.Unwrap())
	})

	// io.Copy prefers ReadFrom when the destination offers it, which would put
	// bytes on the wire without passing through Write and leave the record
	// empty. Not implementing ReaderFrom is what keeps io.Copy honest.
	T.Run("io.Copy routes through Write", func(t *testing.T) {
		t.Parallel()

		wrapped, rec := newRecorder(newPlainWriter(), 1024)

		_, ok := wrapped.(io.ReaderFrom)
		test.False(t, ok)

		_, err := io.Copy(wrapped, strings.NewReader("copied"))
		must.NoError(t, err)

		test.EqOp(t, "copied", string(rec.response(nil).Body))
	})
}

// TestRecorder_OptionalInterfaces is the reason the recorder is built from
// several concrete types instead of one. Handlers feature-detect with
// w.(http.Flusher) and w.(http.Hijacker); a wrapper that implemented both
// unconditionally would answer yes for a writer that supports neither, and
// break the detection it was meant to preserve.
func TestRecorder_OptionalInterfaces(T *testing.T) {
	T.Parallel()

	cases := []struct {
		build     func() http.ResponseWriter
		name      string
		canFlush  bool
		canHijack bool
	}{
		{name: "neither", build: func() http.ResponseWriter { return newPlainWriter() }},
		{
			name:     "flusher only",
			build:    func() http.ResponseWriter { return &flusherWriter{plainWriter: newPlainWriter()} },
			canFlush: true,
		},
		{
			name:      "hijacker only",
			build:     func() http.ResponseWriter { return &hijackerWriter{plainWriter: newPlainWriter()} },
			canHijack: true,
		},
		{
			name:      "both",
			build:     func() http.ResponseWriter { return &bothWriter{plainWriter: newPlainWriter()} },
			canFlush:  true,
			canHijack: true,
		},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			wrapped, _ := newRecorder(tc.build(), 1024)

			_, isFlusher := wrapped.(http.Flusher)
			_, isHijacker := wrapped.(http.Hijacker)

			test.EqOp(t, tc.canFlush, isFlusher)
			test.EqOp(t, tc.canHijack, isHijacker)
		})
	}

	T.Run("forwards Flush to the base writer", func(t *testing.T) {
		t.Parallel()

		base := &flusherWriter{plainWriter: newPlainWriter()}
		wrapped, _ := newRecorder(base, 1024)

		flusher, ok := wrapped.(http.Flusher)
		must.True(t, ok)
		flusher.Flush()

		test.True(t, base.flushed)
	})

	T.Run("forwards Flush on a writer that also hijacks", func(t *testing.T) {
		t.Parallel()

		base := &bothWriter{plainWriter: newPlainWriter()}
		wrapped, _ := newRecorder(base, 1024)

		flusher, ok := wrapped.(http.Flusher)
		must.True(t, ok)
		flusher.Flush()

		test.True(t, base.flushed)
	})

	// A real net/http writer supports both, and ResponseController is the
	// modern way to reach them. It follows Unwrap, so it keeps working for
	// capabilities added after this code was written.
	T.Run("ResponseController reaches the real writer through Unwrap", func(t *testing.T) {
		t.Parallel()

		done := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			defer close(done)

			wrapped, _ := newRecorder(res, 1024)
			_, _ = wrapped.Write([]byte("chunk"))

			test.NoError(t, http.NewResponseController(wrapped).Flush())
		}))
		t.Cleanup(srv.Close)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, http.NoBody)
		must.NoError(t, err)

		res, err := srv.Client().Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		body, err := io.ReadAll(res.Body)
		must.NoError(t, err)
		test.EqOp(t, "chunk", string(body))

		<-done
	})
}

func TestWriteResponse(T *testing.T) {
	T.Parallel()

	T.Run("replays status, headers, and body", func(t *testing.T) {
		t.Parallel()

		res := httptest.NewRecorder()

		writeResponse(res, &Response{
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"ok":true}`),
			StatusCode: http.StatusCreated,
		}, ReplayHeader)

		test.EqOp(t, http.StatusCreated, res.Code)
		test.EqOp(t, "application/json", res.Header().Get("Content-Type"))
		test.EqOp(t, "true", res.Header().Get(ReplayHeader))
		test.EqOp(t, `{"ok":true}`, res.Body.String())
	})

	T.Run("marks an omitted body and sends none", func(t *testing.T) {
		t.Parallel()

		res := httptest.NewRecorder()

		writeResponse(res, &Response{StatusCode: http.StatusCreated, Truncated: true}, ReplayHeader)

		test.EqOp(t, http.StatusCreated, res.Code)
		test.EqOp(t, "true", res.Header().Get(BodyOmittedHeader))
		test.EqOp(t, "", res.Body.String())
	})

	T.Run("an empty replay header name suppresses the marker", func(t *testing.T) {
		t.Parallel()

		res := httptest.NewRecorder()

		writeResponse(res, &Response{StatusCode: http.StatusOK}, "")

		test.EqOp(t, "", res.Header().Get(ReplayHeader))
	})
}

// failingWriter fails every Write, standing in for a client that vanished
// between the header and the body.
type failingWriter struct {
	*plainWriter
	err error
}

func (w *failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteResponse_WriteFailure(T *testing.T) {
	T.Parallel()

	// The status line is already gone by the time a body write fails, so the
	// error is reported to the caller to log rather than turned into a
	// response nobody can receive.
	T.Run("surfaces a failure to write the body", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("connection reset")
		res := &failingWriter{plainWriter: newPlainWriter(), err: boom}

		err := writeResponse(res, &Response{StatusCode: http.StatusCreated, Body: []byte("body")}, ReplayHeader)

		test.ErrorIs(t, err, boom)
	})

	T.Run("reports no error when there is no body to write", func(t *testing.T) {
		t.Parallel()

		res := &failingWriter{plainWriter: newPlainWriter(), err: platformerrors.New("unused")}

		test.NoError(t, writeResponse(res, &Response{StatusCode: http.StatusNoContent}, ReplayHeader))
	})
}

func TestRecorder_Hijack(T *testing.T) {
	T.Parallel()

	// Hijacking is how a handler takes over the connection for websockets. The
	// recorder has to pass it through rather than swallow it, on both variants
	// that carry it.
	T.Run("forwards Hijack to the base writer", func(t *testing.T) {
		t.Parallel()

		for name, base := range map[string]http.ResponseWriter{
			"hijacker only": &hijackerWriter{plainWriter: newPlainWriter()},
			"both":          &bothWriter{plainWriter: newPlainWriter()},
		} {
			wrapped, _ := newRecorder(base, 1024)

			hijacker, ok := wrapped.(http.Hijacker)
			must.True(t, ok, must.Sprintf("%s should expose Hijacker", name))

			conn, rw, err := hijacker.Hijack()
			test.NoError(t, err, test.Sprintf("%s", name))
			test.Nil(t, conn)
			test.Nil(t, rw)
		}
	})
}

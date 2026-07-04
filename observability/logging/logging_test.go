package logging

import (
	"errors"
	"net/http"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestAllLevels(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		levels := AllLevels()
		test.SliceNotEmpty(t, levels)
		test.SliceContains(t, levels, InfoLevel)
		test.SliceContains(t, levels, DebugLevel)
		test.SliceContains(t, levels, ErrorLevel)
		test.SliceContains(t, levels, WarnLevel)
	})
}

func TestEnsureLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, EnsureLogger(noopLoggerSingleton))
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, EnsureLogger(nil))
	})
}

func TestNewNamedLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewNamedLogger(noopLoggerSingleton, "test"))
	})

	T.Run("with nil logger", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewNamedLogger(nil, "test"))
	})
}

func TestNoopLogger(T *testing.T) {
	T.Parallel()

	T.Run("NewNoopLogger", func(t *testing.T) {
		t.Parallel()

		l := noopLoggerSingleton
		test.NotNil(t, l)
	})

	T.Run("Info", func(t *testing.T) {
		t.Parallel()

		noopLoggerSingleton.Info("test")
	})

	T.Run("Debug", func(t *testing.T) {
		t.Parallel()

		noopLoggerSingleton.Debug("test")
	})

	T.Run("Error", func(t *testing.T) {
		t.Parallel()

		noopLoggerSingleton.Error("test", errors.New("blah"))
	})

	T.Run("SetRequestIDFunc", func(t *testing.T) {
		t.Parallel()

		noopLoggerSingleton.SetRequestIDFunc(func(*http.Request) string { return "" })
	})

	T.Run("WithName", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.WithName("test"))
	})

	T.Run("Clone", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.Clone())
	})

	T.Run("WithValues", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.WithValues(map[string]any{"key": "value"}))
	})

	T.Run("WithValue", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.WithValue("key", "value"))
	})

	T.Run("WithRequest", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com", http.NoBody)
		must.NoError(t, err)

		test.NotNil(t, noopLoggerSingleton.WithRequest(req))
	})

	T.Run("WithResponse", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.WithResponse(&http.Response{}))
	})

	T.Run("WithError", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, noopLoggerSingleton.WithError(errors.New("blah")))
	})

	T.Run("WithSpan", func(t *testing.T) {
		t.Parallel()

		span := noop.NewTracerProvider().Tracer("test").Start
		ctx := t.Context()
		_, s := noop.NewTracerProvider().Tracer("test").Start(ctx, "test")

		_ = span
		test.NotNil(t, noopLoggerSingleton.WithSpan(s))
	})
}

func TestExtractSpanInfo(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		_, span := noop.NewTracerProvider().Tracer("test").Start(ctx, "test")

		info := ExtractSpanInfo(span)
		test.NotEq(t, "", info.SpanID)
		test.NotEq(t, "", info.TraceID)
	})

	T.Run("nil span yields a zero-value SpanInfo instead of panicking", func(t *testing.T) {
		t.Parallel()

		info := ExtractSpanInfo(nil)
		test.EqOp(t, "", info.SpanID)
		test.EqOp(t, "", info.TraceID)
	})
}

func TestExtractRequestInfo(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/path?foo=bar", http.NoBody)
		must.NoError(t, err)

		info := ExtractRequestInfo(req, func(r *http.Request) string { return "req-123" })
		test.EqOp(t, http.MethodGet, info.Method)
		test.EqOp(t, "/path", info.Path)
		test.EqOp(t, "foo=bar", info.Query)
		test.EqOp(t, "req-123", info.RequestID)
	})

	T.Run("with nil request", func(t *testing.T) {
		t.Parallel()

		info := ExtractRequestInfo(nil, nil)
		test.EqOp(t, "", info.Method)
		test.EqOp(t, "", info.Path)
		test.EqOp(t, "", info.Query)
		test.EqOp(t, "", info.RequestID)
	})

	T.Run("with nil URL", func(t *testing.T) {
		t.Parallel()

		req := &http.Request{Method: http.MethodPost}

		info := ExtractRequestInfo(req, nil)
		test.EqOp(t, http.MethodPost, info.Method)
		test.EqOp(t, "", info.Path)
		test.EqOp(t, "", info.Query)
	})

	T.Run("with nil requestIDFunc", func(t *testing.T) {
		t.Parallel()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/path", http.NoBody)
		must.NoError(t, err)

		info := ExtractRequestInfo(req, nil)
		test.EqOp(t, http.MethodGet, info.Method)
		test.EqOp(t, "", info.RequestID)
	})
}

var _ Logger = (*noopLogger)(nil)
var _ trace.Span = trace.Span(nil)

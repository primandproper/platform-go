package otelgrpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

func TestNewLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		test.NotNil(t, l)
		test.NoError(t, err)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), nil)
		test.Nil(t, l)
		test.Error(t, err)
	})

	T.Run("with info level", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.InfoLevel, t.Name(), &Config{})
		test.NotNil(t, l)
		test.NoError(t, err)
	})

	T.Run("with warn level", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.WarnLevel, t.Name(), &Config{})
		test.NotNil(t, l)
		test.NoError(t, err)
	})

	T.Run("with error level", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.ErrorLevel, t.Name(), &Config{})
		test.NotNil(t, l)
		test.NoError(t, err)
	})

	T.Run("with collector endpoint", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: "localhost:4317",
			Insecure:          true,
		}

		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), cfg)
		test.NotNil(t, l)
		test.NoError(t, err)
	})
}

func Test_zerologLogger_WithName(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithName(t.Name()))
	})
}

func Test_zerologLogger_SetRequestIDFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.SetRequestIDFunc(func(*http.Request) string {
			return ""
		})
	})

	T.Run("with nil function", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.SetRequestIDFunc(nil)
	})
}

func Test_zerologLogger_Info(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.Info(t.Name())
	})
}

func Test_zerologLogger_Debug(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.Debug(t.Name())
	})
}

func Test_zerologLogger_Error(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.Error(t.Name(), errors.New("blah"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l.Error(t.Name(), nil)
	})
}

func Test_zerologLogger_Clone(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.Clone())
	})
}

func Test_zerologLogger_WithValue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithValue("name", t.Name()))
	})
}

func Test_zerologLogger_WithValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithValues(map[string]any{"name": t.Name()}))
	})
}

func Test_zerologLogger_WithError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithError(errors.New("blah")))
	})
}

func Test_zerologLogger_WithSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		span := trace.SpanFromContext(ctx)

		test.NotNil(t, l.WithSpan(span))
	})
}

func Test_zerologLogger_WithRequest(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l, ok := logger.(*otelSlogLogger)
		must.True(t, ok)

		l.requestIDFunc = func(*http.Request) string {
			return t.Name()
		}

		u, err := url.ParseRequestURI("https://whatever.whocares.gov/path?things=stuff")
		must.NoError(t, err)

		test.NotNil(t, l.WithRequest(&http.Request{
			URL: u,
		}))
	})

	T.Run("with nil request", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithRequest(nil))
	})
}

func Test_zerologLogger_WithResponse(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithResponse(&http.Response{}))
	})

	T.Run("with nil response", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		test.NotNil(t, l.WithResponse(nil))
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: "localhost:4317",
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("rejects a missing collector endpoint", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func Test_otelSlogLogger_requestIDFuncSurvivesDerivation(T *testing.T) {
	T.Parallel()

	T.Run("WithName-derived logger still emits the request ID", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		root := &otelSlogLogger{logger: slog.New(slog.NewJSONHandler(&buf, nil))}
		root.SetRequestIDFunc(func(*http.Request) string { return "req-123" })

		u, err := url.ParseRequestURI("https://example.com/path?things=stuff")
		must.NoError(t, err)

		root.WithName(t.Name()).
			WithRequest(&http.Request{Method: http.MethodGet, URL: u}).
			Info("hello")

		test.StrContains(t, buf.String(), "req-123")
	})
}

func Test_otelSlogLogger_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("no-op without a collector endpoint", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), &Config{})
		must.NoError(t, err)

		l, ok := logger.(*otelSlogLogger)
		must.True(t, ok)
		test.Nil(t, l.loggerProvider)
		test.NoError(t, l.Shutdown(ctx))
	})

	T.Run("collector path wires a shutdownable provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: "localhost:4317",
			Insecure:          true,
		}

		logger, err := NewOtelSlogLogger(ctx, logging.DebugLevel, t.Name(), cfg)
		must.NoError(t, err)

		l, ok := logger.(*otelSlogLogger)
		must.True(t, ok)
		must.NotNil(t, l.loggerProvider)

		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		test.NoError(t, l.Shutdown(shutdownCtx))
	})
}

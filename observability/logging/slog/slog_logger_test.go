package slog

import (
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/primandproper/platform-go/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

func TestNewLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewSlogLogger(logging.DebugLevel))
	})

	T.Run("with info level", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewSlogLogger(logging.InfoLevel))
	})

	T.Run("with warn level", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewSlogLogger(logging.WarnLevel))
	})

	T.Run("with error level", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewSlogLogger(logging.ErrorLevel))
	})
}

func Test_zerologLogger_WithName(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithName(t.Name()))
	})
}

func Test_zerologLogger_SetRequestIDFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.SetRequestIDFunc(func(*http.Request) string {
			return ""
		})
	})

	T.Run("with nil function", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.SetRequestIDFunc(nil)
	})
}

func Test_zerologLogger_Info(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.Info(t.Name())
	})
}

func Test_zerologLogger_Debug(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.Debug(t.Name())
	})
}

func Test_zerologLogger_Error(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.Error(t.Name(), errors.New("blah"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		l.Error(t.Name(), nil)
	})
}

func Test_zerologLogger_Clone(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.Clone())
	})
}

func Test_zerologLogger_WithValue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithValue("name", t.Name()))
	})
}

func Test_zerologLogger_WithValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithValues(map[string]any{"name": t.Name()}))
	})
}

func Test_zerologLogger_WithError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithError(errors.New("blah")))
	})
}

func Test_zerologLogger_WithSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := NewSlogLogger(logging.DebugLevel)

		span := trace.SpanFromContext(ctx)

		test.NotNil(t, l.WithSpan(span))
	})
}

func Test_zerologLogger_WithRequest(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l, ok := NewSlogLogger(logging.DebugLevel).(*slogLogger)
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

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithRequest(nil))
	})
}

func Test_zerologLogger_WithResponse(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithResponse(&http.Response{}))
	})

	T.Run("with nil response", func(t *testing.T) {
		t.Parallel()

		l := NewSlogLogger(logging.DebugLevel)

		test.NotNil(t, l.WithResponse(nil))
	})
}

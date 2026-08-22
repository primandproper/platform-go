package zap

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// mustZapLogger builds a zap logger or fails the test. NewZapLogger reports a
// zap build failure rather than degrading to a noop, so every test that only
// wants a working logger says so once here.
func mustZapLogger(t *testing.T, lvl logging.Level) logging.Logger {
	t.Helper()

	l, err := NewZapLogger(lvl)
	must.NoError(t, err)

	return l
}

func TestNewLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l, err := NewZapLogger(logging.DebugLevel)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("with info level", func(t *testing.T) {
		t.Parallel()

		l, err := NewZapLogger(logging.InfoLevel)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("with warn level", func(t *testing.T) {
		t.Parallel()

		l, err := NewZapLogger(logging.WarnLevel)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("with error level", func(t *testing.T) {
		t.Parallel()

		l, err := NewZapLogger(logging.ErrorLevel)
		test.NoError(t, err)
		test.NotNil(t, l)
	})
}

func Test_zapLogger_WithName(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithName(t.Name()))
	})
}

func Test_zapLogger_SetLevel(T *testing.T) {
	T.Parallel()

	T.Run("with info level", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
		must.True(t, ok)

		l.SetLevel(logging.InfoLevel)
	})

	T.Run("with debug level", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
		must.True(t, ok)

		l.SetLevel(logging.DebugLevel)
	})

	T.Run("with warn level", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
		must.True(t, ok)

		l.SetLevel(logging.WarnLevel)
	})

	T.Run("with error level", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
		must.True(t, ok)

		l.SetLevel(logging.ErrorLevel)
	})

	T.Run("with nil level", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
		must.True(t, ok)

		l.SetLevel("")
	})
}

func Test_zapLogger_SetRequestIDFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.SetRequestIDFunc(func(*http.Request) string {
			return ""
		})
	})

	T.Run("with nil function", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.SetRequestIDFunc(nil)
	})
}

func Test_zapLogger_Info(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.Info(t.Name())
	})
}

func Test_zapLogger_Debug(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.Debug(t.Name())
	})
}

func Test_zapLogger_Warn(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.Warn(t.Name())
	})
}

func Test_zapLogger_warnLevelThreshold(T *testing.T) {
	T.Parallel()

	T.Run("warn sits between info and error", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.WarnLevel).(*Logger)
		must.True(t, ok)

		core := l.logger.Core()
		test.False(t, core.Enabled(zapcore.DebugLevel))
		test.False(t, core.Enabled(zapcore.InfoLevel))
		test.True(t, core.Enabled(zapcore.WarnLevel))
		test.True(t, core.Enabled(zapcore.ErrorLevel))
	})
}

func Test_zapLogger_Error(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.Error(t.Name(), errors.New("blah"))
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		l.Error(t.Name(), nil)
	})
}

func Test_zapLogger_Clone(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.Clone())
	})
}

func Test_zapLogger_WithValue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithValue("name", t.Name()))
	})
}

func Test_zapLogger_WithValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithValues(map[string]any{"name": t.Name()}))
	})
}

func Test_zapLogger_WithError(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithError(errors.New("blah")))
	})
}

func Test_zapLogger_WithSpan(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		l := mustZapLogger(t, logging.DebugLevel)

		span := trace.SpanFromContext(ctx)

		test.NotNil(t, l.WithSpan(span))
	})
}

func Test_zapLogger_WithRequest(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.DebugLevel).(*Logger)
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

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithRequest(nil))
	})
}

func Test_zapLogger_WithResponse(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithResponse(&http.Response{}))
	})

	T.Run("with nil response", func(t *testing.T) {
		t.Parallel()

		l := mustZapLogger(t, logging.DebugLevel)

		test.NotNil(t, l.WithResponse(nil))
	})
}

func Test_zapLogger_SetLevelOnDerivedLogger(T *testing.T) {
	T.Parallel()

	T.Run("derived logger shares the root's atomic level", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
		core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buf), atomicLevel)
		root := &Logger{logger: zap.New(core), atomicLevel: atomicLevel}

		derived, ok := root.WithName(t.Name()).(*Logger)
		must.True(t, ok)

		// Before the fix this nil-dereferenced; now it adjusts the shared atomic level.
		derived.SetLevel(logging.ErrorLevel)

		test.EqOp(t, zap.ErrorLevel, atomicLevel.Level())
	})
}

func Test_zapLogger_NewZapLoggerLevelMapping(T *testing.T) {
	T.Parallel()

	T.Run("warn level maps to warn", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.WarnLevel).(*Logger)
		must.True(t, ok)

		test.EqOp(t, zap.WarnLevel, l.atomicLevel.Level())
	})

	T.Run("error level maps to error", func(t *testing.T) {
		t.Parallel()

		l, ok := mustZapLogger(t, logging.ErrorLevel).(*Logger)
		must.True(t, ok)

		test.EqOp(t, zap.ErrorLevel, l.atomicLevel.Level())
	})
}

func Test_zapLogger_requestIDFuncSurvivesDerivation(T *testing.T) {
	T.Parallel()

	T.Run("WithName-derived logger still emits the request ID", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&buf), zapcore.DebugLevel)
		root := &Logger{logger: zap.New(core), atomicLevel: zap.NewAtomicLevel()}
		root.SetRequestIDFunc(func(*http.Request) string { return "req-123" })

		u, err := url.ParseRequestURI("https://example.com/path?things=stuff")
		must.NoError(t, err)

		root.WithName(t.Name()).
			WithRequest(&http.Request{Method: http.MethodGet, URL: u}).
			Info("hello")

		test.StrContains(t, buf.String(), "req-123")
	})
}

package totp

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("no options leaves every field unset", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o)
		test.Nil(t, o.tracerProvider)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithTracerProvider(tracingnoop.NewTracerProvider()), nil})

		must.NotNil(t, o)
		test.NotNil(t, o.tracerProvider)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.tracerProvider)
	})
}

func TestWithTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("sets the tracer provider", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithTracerProvider(tracingnoop.NewTracerProvider())})

		must.NotNil(t, o)
		test.NotNil(t, o.tracerProvider)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithTracerProvider(tracingnoop.NewTracerProvider()), WithTracerProvider(nil)})

		must.NotNil(t, o)
		test.Nil(t, o.tracerProvider)
	})
}

func TestWithLogger(T *testing.T) {
	T.Parallel()

	T.Run("sets the logger", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithLogger(loggingnoop.NewLogger())})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithLogger(loggingnoop.NewLogger()), WithLogger(nil)})

		must.NotNil(t, o)
		test.Nil(t, o.logger)
	})
}

package grpc

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
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger()), nil})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
	})

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		})

		must.NotNil(t, o)
		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
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

func TestWithMaxReceiveMessageSize(T *testing.T) {
	T.Parallel()

	T.Run("sets the receive bound", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMaxReceiveMessageSize(1 << 20)})

		must.NotNil(t, o)
		test.EqOp(t, 1<<20, o.maxReceiveMessageSize)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMaxReceiveMessageSize(1 << 20), WithMaxReceiveMessageSize(2 << 20)})

		must.NotNil(t, o)
		test.EqOp(t, 2<<20, o.maxReceiveMessageSize)
	})
}

func TestWithMaxSendMessageSize(T *testing.T) {
	T.Parallel()

	T.Run("sets the send bound", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMaxSendMessageSize(1 << 20)})

		must.NotNil(t, o)
		test.EqOp(t, 1<<20, o.maxSendMessageSize)
	})

	T.Run("last option wins", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithMaxSendMessageSize(1 << 20), WithMaxSendMessageSize(2 << 20)})

		must.NotNil(t, o)
		test.EqOp(t, 2<<20, o.maxSendMessageSize)
	})
}

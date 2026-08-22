package workqueue

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewQueueOptions(T *testing.T) {
	T.Parallel()

	T.Run("an empty set leaves everything absent", func(t *testing.T) {
		t.Parallel()

		o := newQueueOptions(nil)

		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.Nil(t, o.keyCodec)
	})

	T.Run("nil options are skipped rather than applied", func(t *testing.T) {
		t.Parallel()

		o := newQueueOptions([]Option{nil, WithLogger(logging.EnsureLogger(nil)), nil})

		test.NotNil(t, o.logger)
	})

	// Options apply in order, so a caller can take a bundle and then override
	// one of its parts.
	T.Run("later options win", func(t *testing.T) {
		t.Parallel()

		first := logging.EnsureLogger(nil)
		second := logging.NewNamedLogger(nil, "second")

		o := newQueueOptions([]Option{WithLogger(first), WithLogger(second)})

		test.EqOp(t, second, o.logger)
	})

	T.Run("a nil codec is ignored so the default survives", func(t *testing.T) {
		t.Parallel()

		o := newQueueOptions([]Option{WithKeyCodec[string](nil)})

		test.Nil(t, o.keyCodec)
	})

	T.Run("a codec is held for the constructor to assert", func(t *testing.T) {
		t.Parallel()

		o := newQueueOptions([]Option{WithKeyCodec[string](upperCodec{})})

		must.NotNil(t, o.keyCodec)

		_, ok := o.keyCodec.(KeyCodec[string])
		test.True(t, ok)
	})
}

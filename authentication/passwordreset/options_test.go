package passwordreset

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha512"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"
	"github.com/primandproper/primitives-go/v2/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.NotNil(t, o.clock)
		must.NotNil(t, o.generator)
		must.NotNil(t, o.hasher)
		test.EqOp(t, DefaultSecretBytes, o.secretBytes)
		test.Nil(t, o.sweepCtx)
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithSecretBytes(64), nil})

		test.EqOp(t, 64, o.secretBytes)
	})
}

func TestWithClock(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c := newFakeClock()
		o := newOptions([]Option{WithClock(c)})

		test.EqOp(t, clock.Clock(c), o.clock)
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithClock(nil)})

		must.NotNil(t, o.clock)
	})
}

func TestWithGenerator(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		g := &failingGenerator{}
		o := newOptions([]Option{WithGenerator(g)})

		test.EqOp(t, random.Generator(g), o.generator)
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithGenerator(nil)})

		must.NotNil(t, o.generator)
	})
}

func TestWithHasher(T *testing.T) {
	T.Parallel()

	// A store whose hasher moved has to compute a different column, or the
	// option is decorative and every deployment stores the same digest.
	T.Run("changes what the digest column holds", func(t *testing.T) {
		t.Parallel()

		def, _ := newTestStore(t)
		wide, _ := newTestStore(t, WithHasher(sha512.NewSHA512Hasher()))

		test.NotEqOp(t, def.Digest("a-token"), wide.Digest("a-token"))
	})

	T.Run("with nil", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithHasher(nil)})

		must.NotNil(t, o.hasher)
	})
}

func TestWithSecretBytes(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, 48, newOptions([]Option{WithSecretBytes(48)}).secretBytes)
		test.EqOp(t, MinimumSecretBytes, newOptions([]Option{WithSecretBytes(MinimumSecretBytes)}).secretBytes)
	})

	// The one place a caller's argument does not win.
	T.Run("refuses a token too short to be one", func(t *testing.T) {
		t.Parallel()

		for _, n := range []int{0, -1, MinimumSecretBytes - 1} {
			test.EqOp(t, DefaultSecretBytes, newOptions([]Option{WithSecretBytes(n)}).secretBytes)
		}
	})
}

func TestWithSweeper(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithSweeper(t.Context(), time.Minute)})

		must.NotNil(t, o.sweepCtx)
		test.EqOp(t, time.Minute, o.sweepInterval)
	})

	T.Run("starts nothing without a context or an interval", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, newOptions([]Option{WithSweeper(nil, time.Minute)}).sweepCtx) //nolint:staticcheck // the nil context is the case under test
		test.Nil(t, newOptions([]Option{WithSweeper(t.Context(), 0)}).sweepCtx)
		test.Nil(t, newOptions([]Option{WithSweeper(t.Context(), -time.Minute)}).sweepCtx)
	})
}

func TestObservabilityOptions(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
	})

	// Absent means noop: a store given none of the three still builds.
	T.Run("with none of them", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, newTestClient(t))
		must.NoError(t, err)
		must.NotNil(t, store.o11y)
		must.NotNil(t, store.sweptCounter)
	})
}

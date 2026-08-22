package audit

import (
	"testing"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// errInstrument is what the failing metrics provider reports.
var errInstrument = platformerrors.New("instrument unavailable")

// failingMetricsProvider fails to build the nth instrument it is asked for, and
// succeeds on every other.
//
// Each constructor here builds its instruments in a fixed order and wraps each
// failure with its own description, so walking n across the count is what proves
// no branch reports another's error — a misattributed wrap in a constructor is
// invisible until someone is reading a boot failure at three in the morning.
func failingMetricsProvider(failAt int) metrics.Provider {
	provider := metrics.EnsureMetricsProvider(nil)
	calls := 0

	guard := func() error {
		calls++
		if calls == failAt {
			return errInstrument
		}

		return nil
	}

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if err := guard(); err != nil {
				return nil, err
			}

			return provider.NewInt64Counter(name, options...)
		},
		NewFloat64HistogramFunc: func(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if err := guard(); err != nil {
				return nil, err
			}

			return provider.NewFloat64Histogram(name, options...)
		},
		NewInt64GaugeFunc: func(name string, options ...metric.Int64GaugeOption) (metrics.Int64Gauge, error) {
			if err := guard(); err != nil {
				return nil, err
			}

			return provider.NewInt64Gauge(name, options...)
		},
	}
}

func TestRecorderOptions(T *testing.T) {
	T.Parallel()

	T.Run("apply what they are given", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metrics.EnsureMetricsProvider(nil)

		r := &ChainRecorder{}
		for _, opt := range []RecorderOption{
			WithRecorderTablePrefix("custom"),
			WithRecorderClock(c),
			WithRecorderLogger(logger),
			WithRecorderTracerProvider(tracerProvider),
			WithRecorderMetricsProvider(metricsProvider),
			WithRedaction("user", Redaction{Drop: []string{"password"}}),
		} {
			opt(r)
		}

		test.EqOp(t, "custom", r.prefix)
		test.EqOp(t, clock.Clock(c), r.clock)
		test.EqOp(t, logger, r.logger)
		test.NotNil(t, r.tracerProvider)
		test.NotNil(t, r.metricsProvider)
		test.MapLen(t, 1, r.redactions)
	})

	T.Run("ignore empty and nil values rather than clobbering a default", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		r := &ChainRecorder{prefix: DefaultTablePrefix, clock: c}

		WithRecorderTablePrefix("")(r)
		WithRecorderClock(nil)(r)

		test.EqOp(t, DefaultTablePrefix, r.prefix)
		test.EqOp(t, clock.Clock(c), r.clock)
	})

	T.Run("reports an instrument that cannot be built", func(t *testing.T) {
		t.Parallel()

		for failAt := 1; failAt <= 2; failAt++ {
			_, err := NewRecorder(dialect.SQLite, WithRecorderMetricsProvider(failingMetricsProvider(failAt)))
			test.ErrorIs(t, err, errInstrument, test.Sprintf("instrument %d", failAt))
		}
	})
}

func TestReaderOptions(T *testing.T) {
	T.Parallel()

	T.Run("apply what they are given", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()

		r := &SQLReader{}
		for _, opt := range []ReaderOption{
			WithReaderTablePrefix("custom"),
			WithReaderLogger(logger),
			WithReaderTracerProvider(tracingnoop.NewTracerProvider()),
			WithReaderMetricsProvider(metrics.EnsureMetricsProvider(nil)),
		} {
			opt(r)
		}

		test.EqOp(t, "custom", r.prefix)
		test.EqOp(t, logger, r.logger)
		test.NotNil(t, r.tracerProvider)
		test.NotNil(t, r.metricsProvider)
	})

	T.Run("ignore an empty prefix", func(t *testing.T) {
		t.Parallel()

		r := &SQLReader{prefix: DefaultTablePrefix}
		WithReaderTablePrefix("")(r)

		test.EqOp(t, DefaultTablePrefix, r.prefix)
	})

	T.Run("reports an instrument that cannot be built", func(t *testing.T) {
		t.Parallel()

		for failAt := 1; failAt <= 2; failAt++ {
			_, err := NewReader(newTestClient(t),
				WithReaderMetricsProvider(failingMetricsProvider(failAt)))
			test.ErrorIs(t, err, errInstrument, test.Sprintf("instrument %d", failAt))
		}
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		r, err := NewReader(newTestClient(t), nil)
		must.NoError(t, err)
		test.NotNil(t, r)
	})
}

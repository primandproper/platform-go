package env

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v5/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	"github.com/primandproper/platform-go/v5/secrets"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var _ secrets.SecretSource = (*envSecretSource)(nil)

// newRecordingSource builds an envSecretSource with a RecordingObserver swapped in, so a
// test can both drive GetSecret and assert which fields it observed.
func newRecordingSource(t *testing.T) (*envSecretSource, *observability.RecordingObserver) {
	t.Helper()

	source, err := NewEnvSecretSource(nil, nil, nil)
	must.NoError(t, err)

	src, ok := source.(*envSecretSource)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	src.o11y = obs

	return src, obs
}

func TestNewEnvSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("with error creating lookup counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_lookups", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		source, err := NewEnvSecretSource(nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_latency_ms", histName)
				return h, errors.New("arbitrary")
			},
		}

		source, err := NewEnvSecretSource(nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestEnvSecretSource_GetSecret(T *testing.T) {
	T.Parallel()

	T.Run("returns set env var", func(t *testing.T) {
		t.Parallel()

		key := "TEST_SECRET_" + t.Name()
		value := "secret-value"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		source, obs := newRecordingSource(t)
		ctx := context.Background()

		got, err := source.GetSecret(ctx, key)
		must.NoError(t, err)
		test.EqOp(t, value, got)

		// The lookup key is observed; the secret value must never be.
		obs.ObservedOperationWithData(t, map[string]any{
			"secret_key": key,
		})
	})

	T.Run("errors for unset env var", func(t *testing.T) {
		t.Parallel()

		key := "TEST_SECRET_UNSET_" + t.Name()
		must.NoError(t, os.Unsetenv(key))

		source, err := NewEnvSecretSource(nil, nil, nil)
		must.NoError(t, err)
		ctx := context.Background()

		got, err := source.GetSecret(ctx, key)
		test.Error(t, err)
		test.True(t, errors.Is(err, secrets.ErrSecretNotFound))
		test.EqOp(t, "", got)
	})

	T.Run("returns empty for set-but-empty env var", func(t *testing.T) {
		t.Parallel()

		key := "TEST_SECRET_EMPTY_" + t.Name()
		must.NoError(t, os.Setenv(key, ""))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		source, err := NewEnvSecretSource(nil, nil, nil)
		must.NoError(t, err)
		ctx := context.Background()

		got, err := source.GetSecret(ctx, key)
		must.NoError(t, err)
		test.EqOp(t, "", got)
	})
}

func TestEnvSecretSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		source, err := NewEnvSecretSource(nil, nil, nil)
		must.NoError(t, err)

		err = source.Close()
		must.NoError(t, err)
	})
}

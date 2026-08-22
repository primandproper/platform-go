package gcp

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	"github.com/primandproper/platform-go/v13/secrets"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingSource builds a SecretSource with a RecordingObserver swapped in, so a
// test can both drive GetSecret and assert which fields it observed.
func newRecordingSource(t *testing.T, cfg *Config, mc *mockGCPClient) (*SecretSource, *observability.RecordingObserver) {
	t.Helper()

	source, err := NewSecretSource(context.Background(), cfg, mc)
	must.NoError(t, err)
	must.NotNil(t, source)

	obs := observability.NewRecordingObserver()
	source.o11y = obs

	return source, obs
}

func TestNewSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		source, err := NewSecretSource(context.Background(), nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "config is required")
	})

	T.Run("missing ProjectID returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: ""}
		source, err := NewSecretSource(context.Background(), cfg, nil)
		must.Error(t, err)
		test.Nil(t, source)
	})

	T.Run("with mock client succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{value: "secret-value"}
		source, err := NewSecretSource(context.Background(), cfg, mc)
		must.NoError(t, err)
		must.NotNil(t, source)
		defer source.Close()
	})

	T.Run("with error creating lookup counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_lookups", counterName)
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{ProjectID: "test-project"}
		source, err := NewSecretSource(context.Background(), cfg, &mockGCPClient{}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_lookups":
					return metricstest.Int64Counter(t, "x"), nil
				case name + "_errors":
					return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		cfg := &Config{ProjectID: "test-project"}
		source, err := NewSecretSource(context.Background(), cfg, &mockGCPClient{}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_latency_ms", histName)
				return h, errors.New("arbitrary")
			},
		}

		cfg := &Config{ProjectID: "test-project"}
		source, err := NewSecretSource(context.Background(), cfg, &mockGCPClient{}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestGCPSecretSource_GetSecret(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{value: "my-secret-value"}
		source, obs := newRecordingSource(t, cfg, mc)
		defer source.Close()

		got, err := source.GetSecret(t.Context(), "MY_SECRET")
		must.NoError(t, err)
		test.EqOp(t, "my-secret-value", got)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.SecretNameKey: "MY_SECRET",
		})
	})

	T.Run("error from client", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{err: errors.New("gcp error")}
		source, obs := newRecordingSource(t, cfg, mc)
		defer source.Close()

		_, err := source.GetSecret(t.Context(), "MY_SECRET")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "gcp error")

		// Even though the lookup failed, the secret key must still have been
		// observed, and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.SecretNameKey: "MY_SECRET",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("nil payload returns not-found error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{nilPayload: true}
		source, obs := newRecordingSource(t, cfg, mc)
		defer source.Close()

		got, err := source.GetSecret(t.Context(), "MISSING_SECRET")
		must.Error(t, err)
		test.True(t, errors.Is(err, secrets.ErrSecretNotFound))
		test.EqOp(t, "", got)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.SecretNameKey: "MISSING_SECRET",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("full resource name passed through", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{value: "full-name-secret"}
		source, err := NewSecretSource(context.Background(), cfg, mc)
		must.NoError(t, err)
		defer source.Close()

		got, err := source.GetSecret(context.Background(), "projects/other-project/secrets/foo/versions/latest")
		must.NoError(t, err)
		test.EqOp(t, "full-name-secret", got)
	})
}

func TestGCPSecretSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ProjectID: "test-project"}
		mc := &mockGCPClient{}
		source, err := NewSecretSource(context.Background(), cfg, mc)
		must.NoError(t, err)

		err = source.Close()
		must.NoError(t, err)
		test.True(t, mc.closed)
	})
}

type mockGCPClient struct {
	err        error
	value      string
	closed     bool
	nilPayload bool
}

func (m *mockGCPClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.nilPayload {
		return &secretmanagerpb.AccessSecretVersionResponse{}, nil
	}
	return &secretmanagerpb.AccessSecretVersionResponse{
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(m.value)},
	}, nil
}

func (m *mockGCPClient) Close() error {
	m.closed = true
	return nil
}

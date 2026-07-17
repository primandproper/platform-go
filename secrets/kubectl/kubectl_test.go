package kubectl

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v5/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewKubectlSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		source, err := NewKubectlSecretSource(context.Background(), nil, nil, nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "config is required")
	})

	T.Run("missing namespace returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		source, err := NewKubectlSecretSource(context.Background(), cfg, nil, nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
	})

	T.Run("with mock client succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewKubectlSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)
	})

	T.Run("with error creating lookup counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_lookups", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{Namespace: "default"}
		source, err := NewKubectlSecretSource(context.Background(), cfg, &mockSecretGetter{}, nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_lookups":
					return metrics.Int64CounterForTest(t, "x"), nil
				case name + "_errors":
					return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		cfg := &Config{Namespace: "default"}
		source, err := NewKubectlSecretSource(context.Background(), cfg, &mockSecretGetter{}, nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
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

		cfg := &Config{Namespace: "default"}
		source, err := NewKubectlSecretSource(context.Background(), cfg, &mockSecretGetter{}, nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

// newRecordingSource builds a kubectlSecretSource with a RecordingObserver swapped in, so a
// test can both drive GetSecret and assert which identifiers it observed.
func newRecordingSource(t *testing.T, cfg *Config, client SecretGetter) (*kubectlSecretSource, *observability.RecordingObserver) {
	t.Helper()

	source, err := NewKubectlSecretSource(context.Background(), cfg, client, nil, nil, nil)
	must.NoError(t, err)

	k, ok := source.(*kubectlSecretSource)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	k.o11y = obs

	return k, obs
}

func TestKubectlSecretSource_GetSecret(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"password": []byte("s3cret"),
				},
			},
		}
		source, obs := newRecordingSource(t, cfg, mc)

		got, err := source.GetSecret(t.Context(), "db-creds/password")
		must.NoError(t, err)
		test.EqOp(t, "s3cret", got)
		test.EqOp(t, "db-creds", mc.lastName)

		obs.ObservedOperationWithData(t, map[string]any{
			"secret.name": "db-creds",
			"secret.key":  "password",
		})
	})

	T.Run("missing slash in name", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewKubectlSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		_, err = source.GetSecret(context.Background(), "no-slash")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "expected format")
	})

	T.Run("key not found", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{
			secret: &corev1.Secret{
				Data: map[string][]byte{
					"username": []byte("admin"),
				},
			},
		}
		source, obs := newRecordingSource(t, cfg, mc)

		_, err := source.GetSecret(t.Context(), "db-creds/password")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "key \"password\" not found")

		// The identifiers must still have been observed even though the lookup failed.
		obs.ObservedOperationWithData(t, map[string]any{
			"secret.name": "db-creds",
			"secret.key":  "password",
		})
	})

	T.Run("client error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{err: errors.New("k8s api error")}
		source, obs := newRecordingSource(t, cfg, mc)

		_, err := source.GetSecret(t.Context(), "db-creds/password")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "k8s api error")

		// Even though the send failed, the identifiers must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"secret.name": "db-creds",
			"secret.key":  "password",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestKubectlSecretSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewKubectlSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		err = source.Close()
		must.NoError(t, err)
	})
}

func TestResolveName(T *testing.T) {
	T.Parallel()

	T.Run("valid name", func(t *testing.T) {
		t.Parallel()
		secretName, key, err := resolveName("my-secret/my-key")
		must.NoError(t, err)
		test.EqOp(t, "my-secret", secretName)
		test.EqOp(t, "my-key", key)
	})

	T.Run("name with multiple slashes", func(t *testing.T) {
		t.Parallel()
		secretName, key, err := resolveName("my-secret/nested/key")
		must.NoError(t, err)
		test.EqOp(t, "my-secret", secretName)
		test.EqOp(t, "nested/key", key)
	})

	T.Run("no slash", func(t *testing.T) {
		t.Parallel()
		_, _, err := resolveName("no-slash")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "expected format")
	})
}

type mockSecretGetter struct {
	secret   *corev1.Secret
	err      error
	lastName string
}

func (m *mockSecretGetter) Get(_ context.Context, name string, _ metav1.GetOptions) (*corev1.Secret, error) {
	m.lastName = name
	if m.err != nil {
		return nil, m.err
	}
	return m.secret, nil
}

package kubernetes

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

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestNewSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		source, err := NewSecretSource(context.Background(), nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "config is required")
	})

	T.Run("missing namespace returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		source, err := NewSecretSource(context.Background(), cfg, nil)
		must.Error(t, err)
		test.Nil(t, source)
	})

	T.Run("with mock client succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewSecretSource(context.Background(), cfg, mc)
		must.NoError(t, err)
		must.NotNil(t, source)
	})

	T.Run("with error creating lookup counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_lookups", counterName)
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{Namespace: "default"}
		source, err := NewSecretSource(context.Background(), cfg, &mockSecretGetter{}, WithMetricsProvider(mp))
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

		cfg := &Config{Namespace: "default"}
		source, err := NewSecretSource(context.Background(), cfg, &mockSecretGetter{}, WithMetricsProvider(mp))
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

		cfg := &Config{Namespace: "default"}
		source, err := NewSecretSource(context.Background(), cfg, &mockSecretGetter{}, WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

// newRecordingSource builds a SecretSource with a RecordingObserver swapped in, so a
// test can both drive GetSecret and assert which identifiers it observed.
func newRecordingSource(t *testing.T, cfg *Config, client SecretGetter) (*SecretSource, *observability.RecordingObserver) {
	t.Helper()

	source, err := NewSecretSource(context.Background(), cfg, client)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	source.o11y = obs

	return source, obs
}

func TestKubernetesSecretSource_GetSecret(T *testing.T) {
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
			keys.SecretNameKey:  "db-creds",
			keys.SecretEntryKey: "password",
		})
	})

	T.Run("missing slash in name", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewSecretSource(context.Background(), cfg, mc)
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
		// A missing key in an existing secret is the same answer, to the caller,
		// as a missing secret.
		test.ErrorIs(t, err, secrets.ErrSecretNotFound)
		test.StrContains(t, err.Error(), "key \"password\"")

		// The identifiers must still have been observed even though the lookup failed.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.SecretNameKey:  "db-creds",
			keys.SecretEntryKey: "password",
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
			keys.SecretNameKey:  "db-creds",
			keys.SecretEntryKey: "password",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestKubernetesSecretSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Namespace: "default"}
		mc := &mockSecretGetter{}
		source, err := NewSecretSource(context.Background(), cfg, mc)
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

func TestKubernetesSecretSource_GetSecret_ErrorMapping(T *testing.T) {
	T.Parallel()

	T.Run("a missing secret maps to the platform sentinel", func(t *testing.T) {
		t.Parallel()

		mc := &mockSecretGetter{
			err: apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "db-creds"),
		}
		source, _ := newRecordingSource(t, &Config{Namespace: "default"}, mc)

		got, err := source.GetSecret(t.Context(), "db-creds/password")
		// Mapped rather than passed through, so a caller can tell "no such
		// secret" from "could not reach the provider" without knowing which
		// provider it got.
		test.ErrorIs(t, err, secrets.ErrSecretNotFound)
		test.EqOp(t, "", got)
	})

	T.Run("a missing secret preserves the underlying cause", func(t *testing.T) {
		t.Parallel()

		underlying := apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, "db-creds")
		mc := &mockSecretGetter{err: underlying}
		source, _ := newRecordingSource(t, &Config{Namespace: "default"}, mc)

		_, err := source.GetSecret(t.Context(), "db-creds/password")
		test.ErrorIs(t, err, underlying)
	})

	T.Run("an unreachable API server is not reported as not-found", func(t *testing.T) {
		t.Parallel()

		underlying := errors.New("connection refused")
		mc := &mockSecretGetter{err: underlying}
		source, _ := newRecordingSource(t, &Config{Namespace: "default"}, mc)

		got, err := source.GetSecret(t.Context(), "db-creds/password")
		must.Error(t, err)
		// The distinction the sentinel exists to make: absent is not unreachable.
		test.False(t, errors.Is(err, secrets.ErrSecretNotFound))
		test.ErrorIs(t, err, underlying)
		test.EqOp(t, "", got)
	})
}

package secretscfg

import (
	"context"
	"errors"
	"os"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	"github.com/primandproper/platform-go/v13/secrets"
	"github.com/primandproper/platform-go/v13/secrets/gcp"
	"github.com/primandproper/platform-go/v13/secrets/kubernetes"
	"github.com/primandproper/platform-go/v13/secrets/ssm"

	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsssm "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type mockGCPClient struct {
	value string
}

func (m *mockGCPClient) AccessSecretVersion(ctx context.Context, req *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	return &secretmanagerpb.AccessSecretVersionResponse{
		Payload: &secretmanagerpb.SecretPayload{Data: []byte(m.value)},
	}, nil
}

func (m *mockGCPClient) Close() error { return nil }

type mockSSMClient struct {
	value string
}

func (m *mockSSMClient) GetParameter(ctx context.Context, params *awsssm.GetParameterInput, optFns ...func(*awsssm.Options)) (*awsssm.GetParameterOutput, error) {
	return &awsssm.GetParameterOutput{
		Parameter: &types.Parameter{
			Value: aws.String(m.value),
		},
	}, nil
}

type mockKubernetesClient struct {
	secret *corev1.Secret
}

func (m *mockKubernetesClient) Get(_ context.Context, _ string, _ metav1.GetOptions) (*corev1.Secret, error) {
	return m.secret, nil
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid env provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderEnv}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("valid noop provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderNoop}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("valid gcp provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderGCP, GCP: &gcp.Config{ProjectID: "my-project"}}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("invalid gcp provider missing config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderGCP}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("valid ssm provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderSSM, SSM: &ssm.Config{Region: "us-east-1"}}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("invalid ssm provider missing config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderSSM}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("valid kubernetes provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderKubernetes, Kubernetes: &kubernetes.Config{Namespace: "default"}}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("invalid kubernetes provider missing config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderKubernetes}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("unknown provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: "vault"}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})
}

func TestConfig_NewSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns env source", func(t *testing.T) {
		t.Parallel()

		var cfg *Config
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_NIL_CONFIG_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		test.EqOp(t, value, got)
	})

	T.Run("empty provider returns env source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_EMPTY_PROVIDER_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		test.EqOp(t, value, got)
	})

	T.Run("env provider returns env source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_ENV_PROVIDER_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		test.EqOp(t, value, got)
	})

	T.Run("noop provider returns noop source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "any")
		test.ErrorIs(t, err, secrets.ErrSecretNotFound)
		test.EqOp(t, "", got)
	})

	T.Run("gcp provider with mock client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderGCP,
			GCP:       &gcp.Config{ProjectID: "test-project"},
			GCPClient: &mockGCPClient{value: "gcp-secret-value"},
		}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "MY_SECRET")
		must.NoError(t, err)
		test.EqOp(t, "gcp-secret-value", got)
	})

	T.Run("ssm provider with mock client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderSSM,
			SSM:       &ssm.Config{Region: "us-east-1"},
			SSMClient: &mockSSMClient{value: "ssm-param-value"},
		}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "MY_PARAM")
		must.NoError(t, err)
		test.EqOp(t, "ssm-param-value", got)
	})

	T.Run("kubernetes provider with mock client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:   ProviderKubernetes,
			Kubernetes: &kubernetes.Config{Namespace: "default"},
			KubernetesClient: &mockKubernetesClient{
				secret: &corev1.Secret{
					Data: map[string][]byte{
						"password": []byte("k8s-secret-value"),
					},
				},
			},
		}
		source, err := cfg.NewSecretSource(context.Background())
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "my-secret/password")
		must.NoError(t, err)
		test.EqOp(t, "k8s-secret-value", got)
	})

	T.Run("unknown provider returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "vault"}
		source, err := cfg.NewSecretSource(context.Background())
		must.Error(t, err)
		test.Nil(t, source)
		test.ErrorIs(t, err, platformerrors.ErrUnknownProvider)
	})

	T.Run("gcp provider with nil gcp config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderGCP}
		source, err := cfg.NewSecretSource(context.Background())
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "gcp")
	})

	T.Run("ssm provider with nil ssm config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSM}
		source, err := cfg.NewSecretSource(context.Background())
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "ssm")
	})

	T.Run("kubernetes provider with nil kubernetes config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderKubernetes}
		source, err := cfg.NewSecretSource(context.Background())
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "kubernetes")
	})

	T.Run("nil config with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		var cfg *Config
		source, err := cfg.NewSecretSource(context.Background(), WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("env provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{Provider: ProviderEnv}
		source, err := cfg.NewSecretSource(context.Background(), WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("gcp provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:  ProviderGCP,
			GCP:       &gcp.Config{ProjectID: "test-project"},
			GCPClient: &mockGCPClient{value: "x"},
		}
		source, err := cfg.NewSecretSource(context.Background(), WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("ssm provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:  ProviderSSM,
			SSM:       &ssm.Config{Region: "us-east-1"},
			SSMClient: &mockSSMClient{value: "x"},
		}
		source, err := cfg.NewSecretSource(context.Background(), WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("kubernetes provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:         ProviderKubernetes,
			Kubernetes:       &kubernetes.Config{Namespace: "default"},
			KubernetesClient: &mockKubernetesClient{secret: &corev1.Secret{}},
		}
		source, err := cfg.NewSecretSource(context.Background(), WithMetricsProvider(mp))
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

// TestNewSecretSource_nilInterfaceOnError guards the narrowing from a provider's
// own concrete type back to secrets.SecretSource. Returning a constructor's
// result straight through — `return env.NewSecretSource(...)` — converts a nil
// *env.SecretSource into a non-nil secrets.SecretSource, so a caller that checks
// the returned interface against nil gets a value that panics on first use. The
// error is correct either way, which is what makes this invisible without a
// test.
//
// The assertion has to be `s == nil` rather than test.Nil: test.Nil falls back
// to reflect.Value.IsNil for pointer kinds, which reports a nil pointer boxed in
// a non-nil interface as nil, and so passes against the very bug under test.
func TestNewSecretSource_nilInterfaceOnError(T *testing.T) {
	T.Parallel()

	failingProvider := func() *metricsmock.ProviderMock {
		return &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, errors.New("counter init failure")
			},
		}
	}

	T.Run("env provider via the package function", func(t *testing.T) {
		t.Parallel()

		s, err := NewSecretSource(t.Context(), &Config{Provider: ProviderEnv}, WithMetricsProvider(failingProvider()))
		must.Error(t, err)
		test.True(t, s == nil, test.Sprintf("expected a nil secrets.SecretSource, got a non-nil interface holding %T", s))
	})

	T.Run("nil config falls back to env", func(t *testing.T) {
		t.Parallel()

		s, err := NewSecretSource(t.Context(), nil, WithMetricsProvider(failingProvider()))
		must.Error(t, err)
		test.True(t, s == nil, test.Sprintf("expected a nil secrets.SecretSource, got a non-nil interface holding %T", s))
	})

	T.Run("gcp provider via the config method", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderGCP, GCP: &gcp.Config{ProjectID: "p"}, GCPClient: &mockGCPClient{}}

		s, err := cfg.NewSecretSource(t.Context(), WithMetricsProvider(failingProvider()))
		must.Error(t, err)
		test.True(t, s == nil, test.Sprintf("expected a nil secrets.SecretSource, got a non-nil interface holding %T", s))
	})
}

package secretscfg

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/primandproper/platform-go/v3/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v3/observability/metrics/mock"
	"github.com/primandproper/platform-go/v3/secrets/gcp"
	"github.com/primandproper/platform-go/v3/secrets/kubectl"
	"github.com/primandproper/platform-go/v3/secrets/ssm"

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

type mockKubectlClient struct {
	secret *corev1.Secret
}

func (m *mockKubectlClient) Get(_ context.Context, _ string, _ metav1.GetOptions) (*corev1.Secret, error) {
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

	T.Run("valid kubectl provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderKubectl, Kubectl: &kubectl.Config{Namespace: "default"}}
		must.NoError(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("invalid kubectl provider missing config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: ProviderKubectl}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})

	T.Run("unknown provider", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Provider: "vault"}
		must.Error(t, cfg.ValidateWithContext(context.Background()))
	})
}

func TestConfig_ProvideSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns env source", func(t *testing.T) {
		t.Parallel()

		var cfg *Config
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
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
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
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
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
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
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "any")
		must.NoError(t, err)
		test.EqOp(t, "", got)
	})

	T.Run("gcp provider with mock client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderGCP,
			GCP:       &gcp.Config{ProjectID: "test-project"},
			GCPClient: &mockGCPClient{value: "gcp-secret-value"},
		}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
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
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "MY_PARAM")
		must.NoError(t, err)
		test.EqOp(t, "ssm-param-value", got)
	})

	T.Run("kubectl provider with mock client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderKubectl,
			Kubectl:  &kubectl.Config{Namespace: "default"},
			KubectlClient: &mockKubectlClient{
				secret: &corev1.Secret{
					Data: map[string][]byte{
						"password": []byte("k8s-secret-value"),
					},
				},
			},
		}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "my-secret/password")
		must.NoError(t, err)
		test.EqOp(t, "k8s-secret-value", got)
	})

	T.Run("unknown provider returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "vault"}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "unknown")
	})

	T.Run("gcp provider with nil gcp config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderGCP}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "gcp")
	})

	T.Run("ssm provider with nil ssm config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSM}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "ssm")
	})

	T.Run("kubectl provider with nil kubectl config returns error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderKubectl}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "kubectl")
	})

	T.Run("nil config with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		var cfg *Config
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("env provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{Provider: ProviderEnv}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("gcp provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:  ProviderGCP,
			GCP:       &gcp.Config{ProjectID: "test-project"},
			GCPClient: &mockGCPClient{value: "x"},
		}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("ssm provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:  ProviderSSM,
			SSM:       &ssm.Config{Region: "us-east-1"},
			SSMClient: &mockSSMClient{value: "x"},
		}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("kubectl provider with metrics error", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		cfg := &Config{
			Provider:      ProviderKubectl,
			Kubectl:       &kubectl.Config{Namespace: "default"},
			KubectlClient: &mockKubectlClient{secret: &corev1.Secret{}},
		}
		source, err := cfg.ProvideSecretSource(context.Background(), nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

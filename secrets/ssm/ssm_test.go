package ssm

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v6/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	"github.com/primandproper/platform-go/v6/secrets"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingSource builds an ssmSecretSource with a RecordingObserver swapped
// in, so a test can both drive GetSecret and assert which fields it observed.
func newRecordingSource(t *testing.T, cfg *Config, client GetParameterAPI) (*ssmSecretSource, *observability.RecordingObserver) {
	t.Helper()

	source, err := NewSSMSecretSource(context.Background(), cfg, client, nil, nil, nil)
	must.NoError(t, err)
	must.NotNil(t, source)

	s, ok := source.(*ssmSecretSource)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	s.o11y = obs

	return s, obs
}

func TestNewSSMSecretSource(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns error", func(t *testing.T) {
		t.Parallel()
		source, err := NewSSMSecretSource(context.Background(), nil, nil, nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
		test.StrContains(t, err.Error(), "config is required")
	})

	T.Run("missing Region returns error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: ""}
		source, err := NewSSMSecretSource(context.Background(), cfg, nil, nil, nil, nil)
		must.Error(t, err)
		test.Nil(t, source)
	})

	T.Run("with mock client succeeds", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1"}
		mc := &mockSSMClient{value: "param-value"}
		source, err := NewSSMSecretSource(context.Background(), cfg, mc, nil, nil, nil)
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

		cfg := &Config{Region: "us-east-1"}
		source, err := NewSSMSecretSource(context.Background(), cfg, &mockSSMClient{}, nil, nil, mp)
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

		cfg := &Config{Region: "us-east-1"}
		source, err := NewSSMSecretSource(context.Background(), cfg, &mockSSMClient{}, nil, nil, mp)
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

		cfg := &Config{Region: "us-east-1"}
		source, err := NewSSMSecretSource(context.Background(), cfg, &mockSSMClient{}, nil, nil, mp)
		must.Error(t, err)
		test.Nil(t, source)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestSSMSecretSource_GetSecret(T *testing.T) {
	T.Parallel()

	T.Run("success", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1"}
		mc := &mockSSMClient{value: "my-param-value"}
		source, obs := newRecordingSource(t, cfg, mc)

		got, err := source.GetSecret(t.Context(), "MY_PARAM")
		must.NoError(t, err)
		test.EqOp(t, "my-param-value", got)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.NameKey: "MY_PARAM",
		})
	})

	T.Run("error from client", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1"}
		mc := &mockSSMClient{err: errors.New("ssm error")}
		source, obs := newRecordingSource(t, cfg, mc)

		_, err := source.GetSecret(t.Context(), "MY_PARAM")
		must.Error(t, err)
		test.StrContains(t, err.Error(), "ssm error")

		// Even though the lookup failed, the parameter name must still have been
		// observed, and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.NameKey: "MY_PARAM",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("nil parameter returns not-found error", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1"}
		mc := &mockSSMClient{nilParameter: true}
		source, obs := newRecordingSource(t, cfg, mc)

		got, err := source.GetSecret(t.Context(), "MISSING_PARAM")
		must.Error(t, err)
		test.True(t, errors.Is(err, secrets.ErrSecretNotFound))
		test.EqOp(t, "", got)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.NameKey: "MISSING_PARAM",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("name with prefix", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1", Prefix: "/myapp/"}
		mc := &mockSSMClient{value: "prefixed-value"}
		source, err := NewSSMSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		got, err := source.GetSecret(context.Background(), "MY_PARAM")
		must.NoError(t, err)
		test.EqOp(t, "prefixed-value", got)
		test.EqOp(t, "/myapp/MY_PARAM", mc.lastName)
	})

	T.Run("prefix without trailing slash still gets a separator", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1", Prefix: "/myapp"}
		mc := &mockSSMClient{value: "prefixed-value"}
		source, err := NewSSMSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		got, err := source.GetSecret(context.Background(), "MY_PARAM")
		must.NoError(t, err)
		test.EqOp(t, "prefixed-value", got)
		test.EqOp(t, "/myapp/MY_PARAM", mc.lastName)
	})

	T.Run("name already path", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Region: "us-east-1", Prefix: "/myapp/"}
		mc := &mockSSMClient{value: "path-value"}
		source, err := NewSSMSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		got, err := source.GetSecret(context.Background(), "/existing/path/param")
		must.NoError(t, err)
		test.EqOp(t, "path-value", got)
		test.EqOp(t, "/existing/path/param", mc.lastName)
	})
}

func TestSSMSecretSource_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Region: "us-east-1"}
		mc := &mockSSMClient{}
		source, err := NewSSMSecretSource(context.Background(), cfg, mc, nil, nil, nil)
		must.NoError(t, err)

		err = source.Close()
		must.NoError(t, err)
	})
}

type mockSSMClient struct {
	value        string
	err          error
	lastName     string
	nilParameter bool
}

func (m *mockSSMClient) GetParameter(ctx context.Context, params *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	if params.Name != nil {
		m.lastName = aws.ToString(params.Name)
	}
	if m.err != nil {
		return nil, m.err
	}
	if m.nilParameter {
		return &ssm.GetParameterOutput{}, nil
	}
	return &ssm.GetParameterOutput{
		Parameter: &types.Parameter{
			Value: aws.String(m.value),
		},
	}, nil
}

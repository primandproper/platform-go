package ssm

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	"github.com/primandproper/platform-go/v2/secrets"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

const name = "ssm_secret_source"

// GetParameterAPI abstracts GetParameter for testability.
type GetParameterAPI interface {
	GetParameter(ctx context.Context, params *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

type ssmSecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        GetParameterAPI
	prefix        string
}

// NewSSMSecretSource creates a SecretSource backed by AWS SSM Parameter Store.
// If client is nil, a new client is created using the default credential chain.
func NewSSMSecretSource(ctx context.Context, cfg *Config, client GetParameterAPI, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("ssm secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "ssm secret source")
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	lookupCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_lookups", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating lookup counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	if client != nil {
		return &ssmSecretSource{
			o11y:          observability.NewObserver(name, logger, tracerProvider),
			lookupCounter: lookupCounter,
			errorCounter:  errorCounter,
			latencyHist:   latencyHist,
			client:        client,
			prefix:        cfg.Prefix,
		}, nil
	}

	awsCfg, loadErr := config.LoadDefaultConfig(ctx, config.WithRegion(cfg.Region))
	if loadErr != nil {
		return nil, errors.Wrap(loadErr, "ssm secret source: loading aws config")
	}

	return &ssmSecretSource{
		o11y:          observability.NewObserver(name, logger, tracerProvider),
		lookupCounter: lookupCounter,
		errorCounter:  errorCounter,
		latencyHist:   latencyHist,
		client:        ssm.NewFromConfig(awsCfg),
		prefix:        cfg.Prefix,
	}, nil
}

func (s *ssmSecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		s.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	paramName := s.resolveName(name)
	op.Set(keys.NameKey, paramName)

	input := &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	}

	output, err := s.client.GetParameter(ctx, input)
	if err != nil {
		s.errorCounter.Add(ctx, 1)
		return "", op.Error(err, "getting parameter %q", name)
	}
	if output.Parameter == nil {
		return "", nil
	}

	s.lookupCounter.Add(ctx, 1)

	return aws.ToString(output.Parameter.Value), nil
}

func (s *ssmSecretSource) Close() error {
	return nil
}

func (s *ssmSecretSource) resolveName(name string) string {
	if strings.HasPrefix(name, "/") {
		return name
	}
	if s.prefix != "" {
		return s.prefix + name
	}
	return name
}

// Ensure ssmSecretSource implements secrets.SecretSource.
var _ secrets.SecretSource = (*ssmSecretSource)(nil)

package ssm

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/secrets"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

const name = "ssm_secret_source"

// GetParameterAPI abstracts GetParameter for testability.
type GetParameterAPI interface {
	GetParameter(ctx context.Context, params *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

// SecretSource reads secrets from AWS SSM Parameter Store. It is exported, and
// returned by NewSecretSource, so a caller can depend on this source rather
// than on the interface every provider shares — and so reason about what this
// one alone does: reach one parameter prefix over the network, per lookup.
type SecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errorCounter  metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
	client        GetParameterAPI
	prefix        string
}

// NewSecretSource creates a SecretSource backed by AWS SSM Parameter Store.
// If client is nil, a new client is created using the default credential chain.
func NewSecretSource(ctx context.Context, cfg *Config, client GetParameterAPI, opts ...Option) (*SecretSource, error) {
	if cfg == nil {
		return nil, errors.New("ssm secret source: config is required")
	}
	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "ssm secret source")
	}

	o := newOptions(opts)
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

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
		return &SecretSource{
			o11y:          observability.NewObserver(name, o.logger, o.tracerProvider),
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

	return &SecretSource{
		o11y:          observability.NewObserver(name, o.logger, o.tracerProvider),
		lookupCounter: lookupCounter,
		errorCounter:  errorCounter,
		latencyHist:   latencyHist,
		client:        ssm.NewFromConfig(awsCfg),
		prefix:        cfg.Prefix,
	}, nil
}

func (s *SecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, nil, s.latencyHist)()

	paramName := s.resolveName(name)
	op.Set(keys.SecretNameKey, paramName)

	input := &ssm.GetParameterInput{
		Name:           aws.String(paramName),
		WithDecryption: aws.Bool(true),
	}

	output, err := s.client.GetParameter(ctx, input)
	if err != nil {
		s.errorCounter.Add(ctx, 1)

		// Mapped, not passed through: secrets.ErrSecretNotFound exists so a
		// caller can tell "no such secret" from "could not reach the provider"
		// without knowing which provider it got, and a raw ParameterNotFound
		// breaks that contract the moment the deployment switches backends.
		var notFound *ssmtypes.ParameterNotFound
		if stderrors.As(err, &notFound) {
			return "", op.Error(
				errors.Join(secrets.ErrSecretNotFound, err),
				"getting parameter %q", name,
			)
		}

		return "", op.Error(err, "getting parameter %q", name)
	}
	if output.Parameter == nil {
		s.errorCounter.Add(ctx, 1)
		return "", op.Error(secrets.ErrSecretNotFound, "parameter %q not found", name)
	}

	s.lookupCounter.Add(ctx, 1)

	return aws.ToString(output.Parameter.Value), nil
}

func (s *SecretSource) Close() error {
	return nil
}

func (s *SecretSource) resolveName(name string) string {
	if strings.HasPrefix(name, "/") {
		return name
	}
	if s.prefix != "" {
		// SSM parameter names are '/'-delimited hierarchies; join with exactly one
		// separator so Prefix="/app" + "db_password" resolves to "/app/db_password"
		// rather than "/appdb_password".
		return strings.TrimSuffix(s.prefix, "/") + "/" + name
	}
	return name
}

// Ensure SecretSource implements secrets.SecretSource.
var _ secrets.SecretSource = (*SecretSource)(nil)

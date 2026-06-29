package env

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
	"github.com/primandproper/platform-go/v2/secrets"
)

const name = "env_secret_source"

type envSecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
}

// NewEnvSecretSource returns a SecretSource that reads from environment variables.
func NewEnvSecretSource(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (secrets.SecretSource, error) {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	lookupCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_lookups", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating lookup counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	return &envSecretSource{
		o11y:          observability.NewObserver(name, logger, tracerProvider),
		lookupCounter: lookupCounter,
		latencyHist:   latencyHist,
	}, nil
}

func (e *envSecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	// NOTE: only the secret's lookup key is observed, never its value.
	op.Set("secret_key", name)

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	e.lookupCounter.Add(ctx, 1)

	return os.Getenv(name), nil
}

func (e *envSecretSource) Close() error {
	e.o11y.Logger().Debug("closing env secret source")
	return nil
}

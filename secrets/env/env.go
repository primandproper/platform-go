package env

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
	"github.com/primandproper/platform-go/secrets"
)

const name = "env_secret_source"

type envSecretSource struct {
	logger        logging.Logger
	tracer        tracing.Tracer
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
		logger:        logging.NewNamedLogger(logger, name),
		tracer:        tracing.NewNamedTracer(tracerProvider, name),
		lookupCounter: lookupCounter,
		latencyHist:   latencyHist,
	}, nil
}

func (e *envSecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	_, span := e.tracer.StartSpan(ctx)
	defer span.End()

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	e.lookupCounter.Add(ctx, 1)

	return os.Getenv(name), nil
}

func (e *envSecretSource) Close() error {
	e.logger.Debug("closing env secret source")
	return nil
}

package env

import (
	"context"
	"fmt"
	"os"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/secrets"
)

const name = "env_secret_source"

var _ secrets.SecretSource = (*SecretSource)(nil)

// SecretSource reads secrets from this process's environment. It is exported,
// and returned by NewSecretSource, so a caller can depend on this source rather
// than on the interface every provider shares — and so face only what this one
// can do, which is read a variable that is already in memory: no network, no
// credentials, no per-lookup cost worth caching.
type SecretSource struct {
	o11y          observability.Observer
	lookupCounter metrics.Int64Counter
	errCounter    metrics.Int64Counter
	latencyHist   metrics.Float64Histogram
}

// NewSecretSource returns a SecretSource that reads from environment variables.
func NewSecretSource(opts ...Option) (*SecretSource, error) {
	o := newOptions(opts)
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	lookupCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_lookups", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating lookup counter")
	}

	errCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_lookup_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating lookup error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	return &SecretSource{
		o11y:          observability.NewObserver(name, o.logger, o.tracerProvider),
		lookupCounter: lookupCounter,
		errCounter:    errCounter,
		latencyHist:   latencyHist,
	}, nil
}

func (e *SecretSource) GetSecret(ctx context.Context, name string) (string, error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	// NOTE: only the secret's lookup key is observed, never its value.
	op.Set(keys.SecretNameKey, name)

	defer op.Time(ctx, nil, e.latencyHist)()

	value, ok := os.LookupEnv(name)
	if !ok {
		e.errCounter.Add(ctx, 1)

		return "", op.Error(secrets.ErrSecretNotFound, "environment variable not set")
	}

	// Counted after the lookup, and only when it found something, so _lookups
	// means the same thing here as it does for every other secret source: secrets
	// successfully resolved. Incrementing before the lookup made this backend's
	// _lookups a count of attempts, so the same metric name meant two different
	// things depending on which source a service was configured with.
	e.lookupCounter.Add(ctx, 1)

	return value, nil
}

func (e *SecretSource) Close() error {
	e.o11y.Logger().Debug("closing env secret source")
	return nil
}

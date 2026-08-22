/*
Package webhookscfg assembles the webhook machinery from environment
configuration: the Store both halves share, the Dispatcher applications write
through, and the Worker that delivers.

All three read one Config, so the table prefix the Dispatcher writes to is by
construction the one the Worker claims from. The dialect is not configured here
at all: it comes from the database.Client, so it cannot disagree with the
database the statements actually run against. The circuit-breaker
section configures one breaker per endpoint, built lazily through
circuitbreakingcfg exactly as it would be standalone.
*/
package webhookscfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/httpclient"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/webhooks"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"go.opentelemetry.io/otel/attribute"
)

// Config assembles a webhooks Store, Dispatcher, and Worker.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the five webhook tables. It must match the prefix the
	// migrations were rendered with. Defaults to webhooks.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// CircuitBreaker configures the per-endpoint breakers. Its Name is used as
	// a prefix — each endpoint's breaker is named for the endpoint, so a
	// tripped breaker names the subscriber that tripped it.
	CircuitBreaker circuitbreakingcfg.Config `env:",init" envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreaker,omitzero" yaml:"circuitBreaker,omitempty"`

	// Worker carries the delivery loop's own knobs.
	Worker webhooks.WorkerConfig `env:",init" envPrefix:"WORKER_" json:"worker,omitzero" yaml:"worker,omitempty"`

	// HTTPClient configures the single client every delivery goes through.
	HTTPClient httpclient.Config `env:",init" envPrefix:"HTTP_" json:"httpClient,omitzero" yaml:"httpClient,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = webhooks.DefaultTablePrefix
	}

	cfg.HTTPClient.EnsureDefaults()
	cfg.CircuitBreaker.EnsureDefaults()
	cfg.Worker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested configs are validated through validation.By closures because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// they would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Worker, validation.By(func(any) error {
			return cfg.Worker.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.HTTPClient, validation.By(func(any) error {
			return cfg.HTTPClient.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.CircuitBreaker, validation.By(func(any) error {
			return cfg.CircuitBreaker.ValidateWithContext(ctx)
		})),
	)
}

// NewStore builds the Store both halves share. client must be the database
// holding the webhook tables — the same one the Dispatcher's transactions run
// against.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *webhooks.SQLStore into a
// non-nil webhooks.Store on the error path, and a caller testing the result against
// nil would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (webhooks.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating webhooks config")
	}

	options := newOptions(opts)

	base := []webhooks.SQLStoreOption{
		webhooks.WithTablePrefix(cfg.TablePrefix),
		webhooks.WithStoreLogger(options.logger),
		webhooks.WithStoreTracerProvider(options.tracerProvider),
		webhooks.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := webhooks.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewDispatcher builds a Dispatcher from configuration.
//
// The catalog is a required argument rather than a config field: it is Go data
// describing what the application publishes, and there is no useful way to
// express it in the environment. A dispatcher with an empty catalog rejects
// every event, which is the correct failure but a confusing one to debug, so it
// is passed explicitly here.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *webhooks.StoreDispatcher into a
// non-nil webhooks.Dispatcher on the error path, and a caller testing the result against
// nil would find a dispatcher that panics on first use.
func NewDispatcher(
	ctx context.Context,
	cfg *Config,
	store webhooks.Store,
	catalog webhooks.Catalog,
	opts ...Option,
) (webhooks.Dispatcher, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating webhooks config")
	}

	base := []webhooks.DispatcherOption{webhooks.WithCatalog(catalog)}
	if logger != nil {
		base = append(base, webhooks.WithDispatcherLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, webhooks.WithDispatcherTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, webhooks.WithDispatcherMetricsProvider(metricsProvider))
	}

	d, dispatcherErr := webhooks.NewDispatcher(store, append(base, o.dispatcher...)...)
	if dispatcherErr != nil {
		return nil, dispatcherErr
	}

	return d, nil
}

// NewWorker builds a Worker from configuration, including the shared HTTP
// client and the per-endpoint circuit breakers.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewWorker(
	ctx context.Context,
	cfg *Config,
	store webhooks.Store,
	opts ...Option,
) (*webhooks.Worker, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating webhooks config")
	}

	// One client, built once, shared by every delivery. Tracing is forced on
	// rather than read from the config: a delivery is an outbound call to a
	// third party and is the single most useful span this package emits.
	client, err := httpclient.NewHTTPClient(append(
		cfg.HTTPClient.Options(),
		httpclient.WithTimeout(cfg.Worker.RequestTimeout),
		httpclient.WithTracing(true),
		httpclient.WithLogger(logger),
		httpclient.WithTracerProvider(tracerProvider),
		httpclient.WithMetricsProvider(metricsProvider),
	)...)
	if err != nil {
		return nil, errors.Wrap(err, "building the webhooks HTTP client")
	}

	base := []webhooks.WorkerOption{
		webhooks.WithHTTPClient(client),
		webhooks.WithCircuitBreakerFactory(breakerFactory(ctx, cfg, logger, metricsProvider)),
	}
	if logger != nil {
		base = append(base, webhooks.WithWorkerLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, webhooks.WithWorkerTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, webhooks.WithWorkerMetricsProvider(metricsProvider))
	}

	return webhooks.NewWorker(ctx, &cfg.Worker, store, append(base, o.worker...)...)
}

// breakerFactory builds one circuit breaker per endpoint.
//
// Every breaker shares the configured thresholds but carries the endpoint as a
// metric attribute, so the breaker counters say which subscriber tripped rather
// than only that something did. ctx is the construction context: breakers are
// built lazily, on first delivery to an endpoint, but only ever to register
// instruments.
func breakerFactory(
	ctx context.Context,
	cfg *Config,
	logger logging.Logger,
	metricsProvider metrics.Provider,
) webhooks.CircuitBreakerFactory {
	return func(endpointID string) (circuitbreaking.CircuitBreaker, error) {
		breakerCfg := cfg.CircuitBreaker

		return circuitbreakingcfg.NewCircuitBreaker(
			ctx, &breakerCfg,
			circuitbreakingcfg.WithLogger(logger),
			circuitbreakingcfg.WithMetricsProvider(metricsProvider),
			circuitbreakingcfg.WithMetricAttributes(attribute.String(webhooks.EndpointAttributeKey, endpointID)),
		)
	}
}

// EnsureHTTPClient is the seam a caller reaches for when it wants the worker's
// client for something else — a health probe against a subscriber, say. It
// returns the same kind of client NewWorker builds.
//
// It takes no observability options, and so builds a client that records
// nowhere. That is the honest shape for a helper with no pillars to hand it;
// callers that want the instrumented client should take the one NewWorker built.
func EnsureHTTPClient(cfg *Config) (*http.Client, error) {
	if cfg == nil {
		return httpclient.NewHTTPClient()
	}

	cfg.EnsureDefaults()

	return httpclient.NewHTTPClient(append(
		cfg.HTTPClient.Options(),
		httpclient.WithTimeout(cfg.Worker.RequestTimeout),
		httpclient.WithTracing(true),
	)...)
}

package launchdarkly

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/featureflags"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	ld "github.com/launchdarkly/go-server-sdk/v6"
	"github.com/launchdarkly/go-server-sdk/v6/ldcomponents"
	ofld "github.com/open-feature/go-sdk-contrib/providers/launchdarkly/pkg"
	"github.com/open-feature/go-sdk/openfeature"
)

const (
	serviceName  = "launchdarkly_feature_flag_manager"
	clientDomain = "launchdarkly_feature_flags"
)

var (
	ErrMissingHTTPClient = platformerrors.New("missing HTTP client")
	ErrNilConfig         = platformerrors.New("missing config")
	ErrMissingSDKKey     = platformerrors.New("missing SDK key")
)

type (
	// featureFlagManager implements the feature flag interface using OpenFeature.
	featureFlagManager struct {
		ldClient       *ld.LDClient
		ofClient       *openfeature.Client
		circuitBreaker circuitbreaking.CircuitBreaker
		logger         logging.Logger
		tracer         tracing.Tracer
		evalCounter    metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		latencyHist    metrics.Float64Histogram
	}
)

// NewFeatureFlagManager constructs a new featureFlagManager backed by OpenFeature.
func NewFeatureFlagManager(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, httpClient *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, configModifiers ...func(ld.Config) ld.Config) (featureflags.FeatureFlagManager, error) {
	if httpClient == nil {
		return nil, ErrMissingHTTPClient
	}

	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.SDKKey == "" {
		return nil, ErrMissingSDKKey
	}

	if cfg.InitTimeout == time.Duration(0) {
		cfg.InitTimeout = 5 * time.Second
	}

	ldConfig := ld.Config{
		HTTP: ldcomponents.HTTPConfiguration().HTTPClientFactory(func() *http.Client { return httpClient }),
	}

	for _, modifier := range configModifiers {
		ldConfig = modifier(ldConfig)
	}

	client, err := ld.MakeCustomClient(
		cfg.SDKKey,
		ldConfig,
		cfg.InitTimeout,
	)
	if err != nil {
		return nil, platformerrors.Wrap(err, "error initializing LaunchDarkly client")
	}

	provider := ofld.NewProvider(client)
	if err = openfeature.SetNamedProviderAndWait(clientDomain, provider); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			logger.Error("error closing OpenFeatureFlag client", closeErr)
		}
		return nil, platformerrors.Wrap(err, "failed to set OpenFeature provider")
	}

	ofClient := openfeature.NewClient(clientDomain)

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	evalCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_evaluations", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating eval counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	ffm := &featureFlagManager{
		logger:         logging.NewNamedLogger(logging.EnsureLogger(logger), serviceName),
		circuitBreaker: circuitBreaker,
		tracer:         tracing.NewNamedTracer(tracerProvider, serviceName),
		ldClient:       client,
		ofClient:       ofClient,
		evalCounter:    evalCounter,
		errorCounter:   errorCounter,
		latencyHist:    latencyHist,
	}

	return ffm, nil
}

// toOpenFeatureContext converts a featureflags.EvaluationContext into the SDK's
// own representation. It is the only place this provider crosses the boundary
// between the platform-owned type and the OpenFeature type.
func toOpenFeatureContext(evalCtx featureflags.EvaluationContext) openfeature.EvaluationContext {
	return openfeature.NewEvaluationContext(evalCtx.TargetingKey, evalCtx.Attributes)
}

// CanUseFeature returns whether the supplied evaluation context is permitted to use
// the named feature.
func (f *featureFlagManager) CanUseFeature(ctx context.Context, feature string, evalCtx featureflags.EvaluationContext) (bool, error) {
	_, span := f.tracer.StartSpan(ctx)
	defer span.End()

	logger := f.logger.WithValue(keys.UserIDKey, evalCtx.TargetingKey).WithValue("feature", feature)

	if !f.circuitBreaker.CanProceed() {
		return false, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	result, err := f.ofClient.BooleanValue(ctx, feature, false, toOpenFeatureContext(evalCtx))
	f.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		f.errorCounter.Add(ctx, 1)
		f.circuitBreaker.Failed()
		return false, observability.PrepareAndLogError(err, logger, span, "checking feature flag variation")
	}

	f.evalCounter.Add(ctx, 1)
	f.circuitBreaker.Succeeded()
	return result, nil
}

// GetStringValue returns the string value of a feature flag, falling back to
// defaultValue on error.
func (f *featureFlagManager) GetStringValue(ctx context.Context, feature, defaultValue string, evalCtx featureflags.EvaluationContext) (string, error) {
	_, span := f.tracer.StartSpan(ctx)
	defer span.End()

	logger := f.logger.WithValue(keys.UserIDKey, evalCtx.TargetingKey).WithValue("feature", feature)

	if !f.circuitBreaker.CanProceed() {
		return defaultValue, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	result, err := f.ofClient.StringValue(ctx, feature, defaultValue, toOpenFeatureContext(evalCtx))
	f.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		f.errorCounter.Add(ctx, 1)
		f.circuitBreaker.Failed()
		return defaultValue, observability.PrepareAndLogError(err, logger, span, "checking feature flag string variation")
	}

	f.evalCounter.Add(ctx, 1)
	f.circuitBreaker.Succeeded()
	return result, nil
}

// GetInt64Value returns the int64 value of a feature flag, falling back to
// defaultValue on error.
func (f *featureFlagManager) GetInt64Value(ctx context.Context, feature string, defaultValue int64, evalCtx featureflags.EvaluationContext) (int64, error) {
	_, span := f.tracer.StartSpan(ctx)
	defer span.End()

	logger := f.logger.WithValue(keys.UserIDKey, evalCtx.TargetingKey).WithValue("feature", feature)

	if !f.circuitBreaker.CanProceed() {
		return defaultValue, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	result, err := f.ofClient.IntValue(ctx, feature, defaultValue, toOpenFeatureContext(evalCtx))
	f.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		f.errorCounter.Add(ctx, 1)
		f.circuitBreaker.Failed()
		return defaultValue, observability.PrepareAndLogError(err, logger, span, "checking feature flag int variation")
	}

	f.evalCounter.Add(ctx, 1)
	f.circuitBreaker.Succeeded()
	return result, nil
}

// GetFloat64Value returns the float64 value of a feature flag, falling back to
// defaultValue on error.
func (f *featureFlagManager) GetFloat64Value(ctx context.Context, feature string, defaultValue float64, evalCtx featureflags.EvaluationContext) (float64, error) {
	_, span := f.tracer.StartSpan(ctx)
	defer span.End()

	logger := f.logger.WithValue(keys.UserIDKey, evalCtx.TargetingKey).WithValue("feature", feature)

	if !f.circuitBreaker.CanProceed() {
		return defaultValue, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	result, err := f.ofClient.FloatValue(ctx, feature, defaultValue, toOpenFeatureContext(evalCtx))
	f.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		f.errorCounter.Add(ctx, 1)
		f.circuitBreaker.Failed()
		return defaultValue, observability.PrepareAndLogError(err, logger, span, "checking feature flag float variation")
	}

	f.evalCounter.Add(ctx, 1)
	f.circuitBreaker.Succeeded()
	return result, nil
}

// GetObjectValue returns the object (JSON) value of a feature flag, falling back
// to defaultValue on error.
func (f *featureFlagManager) GetObjectValue(ctx context.Context, feature string, defaultValue any, evalCtx featureflags.EvaluationContext) (any, error) {
	_, span := f.tracer.StartSpan(ctx)
	defer span.End()

	logger := f.logger.WithValue(keys.UserIDKey, evalCtx.TargetingKey).WithValue("feature", feature)

	if !f.circuitBreaker.CanProceed() {
		return defaultValue, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	result, err := f.ofClient.ObjectValue(ctx, feature, defaultValue, toOpenFeatureContext(evalCtx))
	f.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	if err != nil {
		f.errorCounter.Add(ctx, 1)
		f.circuitBreaker.Failed()
		return defaultValue, observability.PrepareAndLogError(err, logger, span, "checking feature flag object variation")
	}

	f.evalCounter.Add(ctx, 1)
	f.circuitBreaker.Succeeded()
	return result, nil
}

// Close closes the LaunchDarkly client.
func (f *featureFlagManager) Close() error {
	return f.ldClient.Close()
}

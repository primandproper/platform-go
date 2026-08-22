package launchdarkly

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/featureflags/internal/openfeatureflags"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

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

var _ featureflags.FeatureFlagManager = (*FeatureFlagManager)(nil)

type (
	// FeatureFlagManager is the LaunchDarkly featureflags.FeatureFlagManager
	// implementation, by way of OpenFeature. It is exported, and returned by
	// NewFeatureFlagManager, so a caller who has chosen LaunchDarkly can depend on
	// that choice rather than on the interface every flag backend shares.
	FeatureFlagManager struct {
		ldClient *ld.LDClient
		// Evaluator is the flag evaluation every OpenFeature-backed provider
		// here does; see featureflags/internal/openfeatureflags. Embedded, so
		// this type still presents the whole featureflags.FeatureFlagManager
		// surface.
		openfeatureflags.Evaluator
	}
)

// NewFeatureFlagManager constructs a LaunchDarkly FeatureFlagManager backed by
// OpenFeature.
func NewFeatureFlagManager(cfg *Config, httpClient *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, opts ...Option) (*FeatureFlagManager, error) {
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

	o := newOptions(opts)

	// Built before anything that can fail: the teardown paths below log, and an
	// absent logger must log nowhere rather than panic on the one path that
	// exists to clean up after another failure.
	o11y := observability.NewObserver(serviceName, o.logger, o.tracerProvider)

	// Create the metric instruments before the client/provider so a counter failure
	// returns without having registered a global provider or opened a client to leak.
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	evalCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_evaluations", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating eval counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}

	// Counted apart from errors because the remedies differ: a missing flag is
	// answered by creating the flag, an error by fixing the provider. A sustained
	// rise here usually means a flag name shipped in code that nobody has created.
	notFoundCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_flags_not_found", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating flag-not-found counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	// Each manager binds its own OpenFeature domain so a second manager can't rebind
	// the domain (and thus the provider/client) out from under an existing one.
	domain := fmt.Sprintf("%s_%s", clientDomain, identifiers.New())

	ldConfig := ld.Config{
		HTTP: ldcomponents.HTTPConfiguration().HTTPClientFactory(func() *http.Client { return httpClient }),
	}

	for _, modifier := range o.configModifiers {
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
	if err = openfeature.SetNamedProviderAndWait(domain, provider); err != nil {
		if closeErr := client.Close(); closeErr != nil {
			o11y.Logger().Error("error closing OpenFeatureFlag client", closeErr)
		}
		return nil, platformerrors.Wrap(err, "failed to set OpenFeature provider")
	}

	ofClient := openfeature.NewClient(domain)

	ffm := &FeatureFlagManager{
		ldClient: client,
		Evaluator: openfeatureflags.Evaluator{
			O11y:            o11y,
			Client:          ofClient,
			CircuitBreaker:  circuitBreaker,
			Domain:          domain,
			EvalCounter:     evalCounter,
			ErrorCounter:    errorCounter,
			NotFoundCounter: notFoundCounter,
			LatencyHist:     latencyHist,
		},
	}

	return ffm, nil
}

// Close closes the LaunchDarkly client and detaches it from OpenFeature's
// process-global provider registry.
//
// Each construction registers a uniquely-named provider in that registry, which
// has no removal API — so without the swap below, every reload cycle left
// another registration holding a reference to a client that had just been
// closed, and the process accumulated them until it exited. Replacing the
// registration with the no-op provider releases the client; the (small,
// clientless) map entry itself is not removable and is left behind.
func (f *FeatureFlagManager) Close() error {
	var errs []error

	if err := f.Detach(); err != nil {
		errs = append(errs, err)
	}

	if err := f.ldClient.Close(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

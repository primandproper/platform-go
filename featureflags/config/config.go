// Package featureflagscfg selects and builds a featureflags.FeatureFlagManager
// from configuration: LaunchDarkly, PostHog, or the noop manager.
//
// The two vendor providers differ only in construction and shutdown — both
// evaluate through the same OpenFeature client underneath — so the choice here
// is about which service holds the flags rather than about how they are read.
// The noop manager answers every flag with the default the caller passed, which
// is why it has to be named: it is indistinguishable from a rollout that has not
// started.
//
// The *http.Client is a dependency rather than an option, so the transport the
// vendor SDK uses stays the deployment's to configure.
package featureflagscfg

import (
	"context"
	"net/http"
	"strings"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/featureflags/launchdarkly"
	"github.com/primandproper/platform-go/v13/featureflags/noop"
	"github.com/primandproper/platform-go/v13/featureflags/posthog"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderLaunchDarkly is used to indicate the LaunchDarkly provider.
	ProviderLaunchDarkly = "launchdarkly"
	// ProviderPostHog is used to indicate the PostHog provider.
	ProviderPostHog = "posthog"
	// ProviderNoop reports every flag as its default. It must be selected
	// deliberately — an unset or typo'd provider is an error, because a manager
	// that silently answers "off" for everything looks like a quiet rollout.
	ProviderNoop = "noop"
)

type (
	// Config configures our feature flag manager.
	Config struct {
		LaunchDarkly   *launchdarkly.Config      `env:",init"    envPrefix:"LAUNCH_DARKLY_"    json:"launchDarkly,omitempty"        yaml:"launchDarkly,omitempty"`
		PostHog        *posthog.Config           `env:",init"    envPrefix:"POSTHOG_"          json:"posthog,omitempty"             yaml:"posthog,omitempty"`
		Provider       string                    `env:"PROVIDER" json:"provider,omitempty"     yaml:"provider,omitempty"`
		CircuitBreaker circuitbreakingcfg.Config `env:",init"    envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	cfg.CircuitBreaker.EnsureDefaults()
}

// ValidateWithContext validates the config.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	// Release the sub-configs env parsing's ",init" allocated and nothing filled in.
	// Without this they reach their own validation, which requires the credentials
	// they would need if they were the selected provider — and rejects every config
	// that named a different one.
	cfgnorm.ZeroToNil(&c.LaunchDarkly)
	cfgnorm.ZeroToNil(&c.PostHog)

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.Required, validation.In(ProviderLaunchDarkly, ProviderPostHog, ProviderNoop)),
		validation.Field(&c.LaunchDarkly, validation.When(c.Provider == ProviderLaunchDarkly, validation.Required)),
		validation.Field(&c.PostHog, validation.When(c.Provider == ProviderPostHog, validation.Required)),
	)
}

// NewFeatureFlagManager builds the configured feature flag manager.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *launchdarkly.FeatureFlagManager into a
// non-nil featureflags.FeatureFlagManager on the error path, and a caller testing the result against
// nil would find a manager that panics on first use.
func (c *Config) NewFeatureFlagManager(ctx context.Context, httpClient *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, opts ...Option) (featureflags.FeatureFlagManager, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	c.EnsureDefaults()

	if err := c.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating feature flag config")
	}

	switch strings.TrimSpace(strings.ToLower(c.Provider)) {
	case ProviderLaunchDarkly:
		m, managerErr := launchdarkly.NewFeatureFlagManager(c.LaunchDarkly, httpClient, circuitBreaker, launchdarkly.WithLogger(logger), launchdarkly.WithTracerProvider(tracerProvider), launchdarkly.WithMetricsProvider(metricsProvider))
		if managerErr != nil {
			return nil, managerErr
		}

		return m, nil
	case ProviderPostHog:
		m, managerErr := posthog.NewFeatureFlagManager(c.PostHog, circuitBreaker, posthog.WithLogger(logger), posthog.WithTracerProvider(tracerProvider), posthog.WithMetricsProvider(metricsProvider))
		if managerErr != nil {
			return nil, managerErr
		}

		return m, nil
	case ProviderNoop:
		return noop.NewFeatureFlagManager(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "feature flag provider %q", c.Provider)
	}
}

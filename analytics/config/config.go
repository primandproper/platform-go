// Package analyticscfg selects and builds an analytics.EventReporter from
// configuration — Segment, PostHog, or the noop reporter — handing the vendor
// implementations a circuit breaker built from the same config.
//
// It also carries ProxySources, per-source configuration keyed by a free-form
// source name, which is what the multisource reporter fans out over. The set of
// sources belongs to the application rather than to this module, so it is a map
// rather than a field per source: adding one is a deployment's business and not
// a change to an exported struct here.
package analyticscfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/analytics/noop"
	"github.com/primandproper/platform-go/v13/analytics/posthog"
	"github.com/primandproper/platform-go/v13/analytics/segment"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	posthogsdk "github.com/posthog/posthog-go"
)

const (
	// ProviderSegment represents Segment.
	ProviderSegment = "segment"
	// ProviderPostHog represents PostHog.
	ProviderPostHog = "posthog"
	// ProviderNoop discards every event. It must be selected deliberately — an
	// unset or typo'd provider is an error, because analytics that silently stop
	// being recorded are only noticed when someone asks a question of the data
	// months later.
	ProviderNoop = "noop"
)

type (
	// SourceConfig is the per-source analytics config (provider + credentials). Used for proxy sources; no ProxySources to avoid recursion.
	SourceConfig struct {
		Segment        *segment.Config           `env:",init"                  envPrefix:"SEGMENT_"           json:"segment,omitempty"        yaml:"segment,omitempty"`
		Posthog        *posthog.Config           `env:",init"                  envPrefix:"POSTHOG_"           json:"posthog,omitempty"        yaml:"posthog,omitempty"`
		Provider       string                    `env:"PROVIDER"               json:"provider,omitempty"      yaml:"provider,omitempty"`
		CircuitBreaker circuitbreakingcfg.Config `envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreaker,omitzero" yaml:"circuitBreaker,omitempty"`
	}

	// ProxySourcesConfig holds per-source analytics config for the analytics
	// proxy gRPC service, keyed by source name.
	//
	// It is a map rather than a struct with a field per source: the set of
	// sources belongs to the application, not to this module, and every source
	// an application adds would otherwise be a breaking change to an exported
	// struct here. Source names are free-form; "ios" and "web" are conventional,
	// not special.
	//
	// Environment parsing populates it from PROXY_SOURCES_<NAME>_* keys.
	ProxySourcesConfig map[string]*SourceConfig

	// Config is the configuration structure.
	Config struct {
		ProxySources ProxySourcesConfig `envPrefix:"PROXY_SOURCES_" json:"proxySources,omitempty" yaml:"proxySources,omitempty"`
		SourceConfig
	}
)

// providers are every provider this package implements. Validation and
// NewCollector both read it.
var providers = []string{ProviderSegment, ProviderPostHog, ProviderNoop}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *SourceConfig) EnsureDefaults() {
	cfg.CircuitBreaker.EnsureDefaults()
}

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	cfg.SourceConfig.EnsureDefaults()

	for _, src := range cfg.ProxySources {
		if src != nil {
			src.EnsureDefaults()
		}
	}
}

// ToMap returns the configured sources keyed by name, skipping nil entries. It
// is what the multisource reporter consumes.
func (p ProxySourcesConfig) ToMap() map[string]*SourceConfig {
	m := make(map[string]*SourceConfig, len(p))
	for name, src := range p {
		if src != nil {
			m[name] = src
		}
	}

	return m
}

// ValidateWithContext validates a SourceConfig: the provider must be known and the
// matching credentials block present, so a proxy source with no provider/key can't
// pass validation and silently degrade to a noop at runtime.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so both
// providers' credentials were required at once and no source could load.
func (cfg *SourceConfig) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Segment" and " posthog " while NewCollector built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "analytics provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Segment, validation.Skip.When(provider != ProviderSegment), validation.Required),
		validation.Field(&cfg.Posthog, validation.Skip.When(provider != ProviderPostHog), validation.Required),
	)
}

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	// The root source goes through the same rules as every proxy source rather than
	// a restatement of them, which is what let the two drift: this one accepted an
	// empty provider and fell through to a noop, while the proxy sources required
	// one to be named.
	if err := cfg.SourceConfig.ValidateWithContext(ctx); err != nil {
		return err
	}

	// Each configured proxy source must itself be valid.
	for name, src := range cfg.ProxySources.ToMap() {
		if err := src.ValidateWithContext(ctx); err != nil {
			return errors.Wrapf(err, "validating %q proxy source", name)
		}
	}

	return nil
}

// NewCollector provides a collector.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *segment.EventReporter into a
// non-nil analytics.EventReporter on the error path, and a caller testing the result against
// nil would find a reporter that panics on first use.
func (cfg *SourceConfig) NewCollector(
	ctx context.Context,
	opts ...Option,
) (analytics.EventReporter, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "analytics provider")
	if err != nil {
		return nil, err
	}

	// Validated here as well as at the composition root, because a proxy source
	// is built straight from its own SourceConfig: the credentials rules were
	// reachable only for whoever validated the whole tree first.
	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating analytics config")
	}

	cb, err := cfg.CircuitBreaker.NewCircuitBreaker(ctx, circuitbreakingcfg.WithLogger(logger), circuitbreakingcfg.WithMetricsProvider(metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "could not create analytics circuit breaker")
	}

	switch provider {
	case ProviderSegment:
		if cfg.Segment == nil {
			return nil, errors.New("segment provider configured but segment config is nil")
		}
		r, reporterErr := segment.NewEventReporter(cfg.Segment.APIToken, cb, segment.WithLogger(logger), segment.WithTracerProvider(tracerProvider), segment.WithMetricsProvider(metricsProvider))
		if reporterErr != nil {
			return nil, reporterErr
		}

		return r, nil
	case ProviderPostHog:
		if cfg.Posthog == nil {
			return nil, errors.New("posthog provider configured but posthog config is nil")
		}
		var modifiers []func(*posthogsdk.Config)
		if cfg.Posthog.Endpoint != "" {
			endpoint := cfg.Posthog.Endpoint
			modifiers = append(modifiers, func(c *posthogsdk.Config) { c.Endpoint = endpoint })
		}
		r, reporterErr := posthog.NewEventReporter(cfg.Posthog.APIKey, cb, posthog.WithLogger(logger), posthog.WithTracerProvider(tracerProvider), posthog.WithMetricsProvider(metricsProvider), posthog.WithConfigModifiers(modifiers...))
		if reporterErr != nil {
			return nil, reporterErr
		}

		return r, nil
	case ProviderNoop:
		return noop.NewEventReporter(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "analytics provider %q", cfg.Provider)
	}
}

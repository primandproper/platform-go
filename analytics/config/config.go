package analyticscfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/analytics"
	"github.com/primandproper/platform-go/v2/analytics/noop"
	"github.com/primandproper/platform-go/v2/analytics/posthog"
	"github.com/primandproper/platform-go/v2/analytics/rudderstack"
	"github.com/primandproper/platform-go/v2/analytics/segment"
	circuitbreakingcfg "github.com/primandproper/platform-go/v2/circuitbreaking/config"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderSegment represents Segment.
	ProviderSegment = "segment"
	// ProviderRudderstack represents Rudderstack.
	ProviderRudderstack = "rudderstack"
	// ProviderPostHog represents PostHog.
	ProviderPostHog = "posthog"
)

type (
	// SourceConfig is the per-source analytics config (provider + credentials). Used for proxy sources; no ProxySources to avoid recursion.
	SourceConfig struct {
		Segment        *segment.Config           `env:",init"                  envPrefix:"SEGMENT_"     json:"segment"`
		Posthog        *posthog.Config           `env:",init"                  envPrefix:"POSTHOG_"     json:"posthog"`
		Rudderstack    *rudderstack.Config       `env:",init"                  envPrefix:"RUDDERSTACK_" json:"rudderstack"`
		Provider       string                    `env:"PROVIDER"               json:"provider"`
		CircuitBreaker circuitbreakingcfg.Config `envPrefix:"CIRCUIT_BREAKER_" json:"circuitBreaker"`
	}

	// ProxySourcesConfig holds per-source analytics config for the analytics proxy gRPC service. Sources are codified: ios and web.
	ProxySourcesConfig struct {
		IOS *SourceConfig `env:",init" envPrefix:"IOS_" json:"ios"`
		Web *SourceConfig `env:",init" envPrefix:"WEB_" json:"web"`
	}

	// Config is the configuration structure.
	Config struct {
		ProxySources ProxySourcesConfig `envPrefix:"PROXY_SOURCES_" json:"proxySources"`
		SourceConfig
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *SourceConfig) EnsureDefaults() {
	cfg.CircuitBreaker.EnsureDefaults()
}

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	cfg.SourceConfig.EnsureDefaults()
	if cfg.ProxySources.IOS != nil {
		cfg.ProxySources.IOS.EnsureDefaults()
	}
	if cfg.ProxySources.Web != nil {
		cfg.ProxySources.Web.EnsureDefaults()
	}
}

// ToMap returns a map of source name to config for use by the multisource reporter. Skips nil entries.
func (p ProxySourcesConfig) ToMap() map[string]*SourceConfig {
	m := make(map[string]*SourceConfig)
	if p.IOS != nil {
		m["ios"] = p.IOS
	}
	if p.Web != nil {
		m["web"] = p.Web
	}
	return m
}

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderSegment, ProviderRudderstack, ProviderPostHog)),
		validation.Field(&cfg.Segment, validation.When(cfg.Provider == ProviderSegment, validation.Required)),
		validation.Field(&cfg.Posthog, validation.When(cfg.Provider == ProviderPostHog, validation.Required)),
		validation.Field(&cfg.Rudderstack, validation.When(cfg.Provider == ProviderRudderstack, validation.Required)),
	)
}

// ProvideCollector provides a collector.
func (cfg *SourceConfig) ProvideCollector(
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (analytics.EventReporter, error) {
	cb, err := cfg.CircuitBreaker.ProvideCircuitBreaker(ctx, logger, metricsProvider)
	if err != nil {
		return nil, errors.Wrap(err, "could not create analytics circuit breaker")
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderSegment:
		if cfg.Segment == nil {
			return nil, errors.New("segment provider configured but segment config is nil")
		}
		return segment.NewSegmentEventReporter(logger, tracerProvider, metricsProvider, cfg.Segment.APIToken, cb)
	case ProviderRudderstack:
		if cfg.Rudderstack == nil {
			return nil, errors.New("rudderstack provider configured but rudderstack config is nil")
		}
		return rudderstack.NewRudderstackEventReporter(logger, tracerProvider, metricsProvider, cfg.Rudderstack, cb)
	case ProviderPostHog:
		if cfg.Posthog == nil {
			return nil, errors.New("posthog provider configured but posthog config is nil")
		}
		return posthog.NewPostHogEventReporter(logger, tracerProvider, metricsProvider, cfg.Posthog.APIKey, cb)
	default:
		logging.EnsureLogger(logger).WithValue("provider", cfg.Provider).Info("no analytics provider configured or unrecognized provider, using noop")
		return noop.NewEventReporter(), nil
	}
}

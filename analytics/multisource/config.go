package multisource

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v4/analytics"
	analyticscfg "github.com/primandproper/platform-go/v4/analytics/config"
	"github.com/primandproper/platform-go/v4/analytics/noop"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"
)

// ProvideMultiSourceEventReporter builds a MultiSourceEventReporter from proxy sources config.
// For each source, attempts to create an EventReporter via ProvideCollector.
// If creation fails (e.g. missing credentials) or provider is unset, uses Noop for that source.
//
// For PostHog: reporters are deduplicated by API key. Sources sharing the same PostHog API key
// reuse a single client (the source name is set as a property on each event), while sources with
// distinct API keys each get their own client so their credentials and circuit breaker are honored.
func ProvideMultiSourceEventReporter(
	ctx context.Context,
	proxySources map[string]*analyticscfg.SourceConfig,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (*MultiSourceEventReporter, error) {
	reporters := make(map[string]analytics.EventReporter)
	log := logging.NewNamedLogger(logger, name)

	if len(proxySources) == 0 {
		log.Info("no analytics proxy sources configured, multisource reporter will be empty")
		return NewMultiSourceEventReporter(reporters, logger, tracerProvider), nil
	}

	postHogReportersByKey := make(map[string]analytics.EventReporter)

	for source, sourceCfg := range proxySources {
		log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("configuring analytics reporter for proxy source")

		provider := strings.ToLower(strings.TrimSpace(sourceCfg.Provider))

		// Deduplicate PostHog reporters by API key: sources sharing a key reuse one client,
		// distinct keys each get their own so credentials and circuit breakers aren't discarded.
		var postHogKey string
		if provider == analyticscfg.ProviderPostHog && sourceCfg.Posthog != nil && sourceCfg.Posthog.APIKey != "" {
			postHogKey = sourceCfg.Posthog.APIKey
			if existing, ok := postHogReportersByKey[postHogKey]; ok {
				log.WithValue("source", source).Info("reusing PostHog reporter for proxy source with matching API key")
				reporters[source] = existing
				continue
			}
		}

		r, err := sourceCfg.ProvideCollector(ctx, log, tracerProvider, metricsProvider)
		if err != nil {
			log.WithValue("source", source).WithValue("reason", err.Error()).Error("failed to create reporter for proxy source, using noop", err)
			reporters[source] = noop.NewEventReporter()
			continue
		}
		if r == nil {
			log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("ProvideCollector returned nil reporter, using noop")
			reporters[source] = noop.NewEventReporter()
			continue
		}

		if postHogKey != "" {
			postHogReportersByKey[postHogKey] = r
		}

		log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("analytics reporter configured for proxy source")
		reporters[source] = r
	}

	return NewMultiSourceEventReporter(reporters, logger, tracerProvider), nil
}

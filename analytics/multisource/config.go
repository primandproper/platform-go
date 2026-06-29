package multisource

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/analytics"
	analyticscfg "github.com/primandproper/platform-go/v2/analytics/config"
	"github.com/primandproper/platform-go/v2/analytics/noop"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"
)

// ProvideMultiSourceEventReporter builds a MultiSourceEventReporter from proxy sources config.
// For each source, attempts to create an EventReporter via ProvideCollector.
// If creation fails (e.g. missing credentials) or provider is unset, uses Noop for that source.
//
// For PostHog: a single API key is shared across all sources. One PostHog client is created and
// reused for every PostHog source; the source name is logged as a property on each event.
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

	var sharedPostHogReporter analytics.EventReporter

	for source, sourceCfg := range proxySources {
		log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("configuring analytics reporter for proxy source")

		provider := strings.ToLower(strings.TrimSpace(sourceCfg.Provider))
		if provider == analyticscfg.ProviderPostHog && sharedPostHogReporter != nil {
			// PostHog uses one API key for all sources; reuse the shared client.
			log.WithValue("source", source).Info("reusing shared PostHog reporter for proxy source")
			reporters[source] = sharedPostHogReporter
			continue
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

		if provider == analyticscfg.ProviderPostHog {
			sharedPostHogReporter = r
		}

		log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("analytics reporter configured for proxy source")
		reporters[source] = r
	}

	return NewMultiSourceEventReporter(reporters, logger, tracerProvider), nil
}

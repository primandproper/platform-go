package multisource

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v13/analytics"
	analyticscfg "github.com/primandproper/platform-go/v13/analytics/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
)

// NewMultiSourceEventReporterFromConfig builds a MultiSourceEventReporter from proxy sources config.
// For each source, it creates an EventReporter via NewCollector. A source that fails to
// construct — missing credentials, an unset provider — fails the whole call rather than
// getting a noop of its own: that substitution outlived the mistake by the life of the
// process, and every event for the source went nowhere while every call returned nil.
//
// For PostHog: reporters are deduplicated by API key. Sources sharing the same PostHog API key
// reuse a single client (the source name is set as a property on each event), while sources with
// distinct API keys each get their own client so their credentials and circuit breaker are honored.
func NewMultiSourceEventReporterFromConfig(
	ctx context.Context,
	proxySources map[string]*analyticscfg.SourceConfig,
	opts ...Option,
) (*MultiSourceEventReporter, error) {
	o := newOptions(opts)

	reporters := make(map[string]analytics.EventReporter)
	log := logging.NewNamedLogger(o.logger, name)

	if len(proxySources) == 0 {
		log.Info("no analytics proxy sources configured, multisource reporter will be empty")
		return NewMultiSourceEventReporter(reporters, opts...), nil
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

		// A source that fails to construct is fatal rather than quietly noop'd:
		// the substitution lasted the whole process lifetime, so every event for
		// that source was dropped until someone redeployed.
		r, err := sourceCfg.NewCollector(ctx,
			analyticscfg.WithLogger(log),
			analyticscfg.WithTracerProvider(o.tracerProvider),
			analyticscfg.WithMetricsProvider(o.metricsProvider))
		if err != nil {
			return nil, errors.Wrapf(err, "creating reporter for proxy source %q", source)
		}

		if r == nil {
			return nil, errors.Newf("reporter for proxy source %q built nothing for provider %q", source, sourceCfg.Provider)
		}

		if postHogKey != "" {
			postHogReportersByKey[postHogKey] = r
		}

		log.WithValue("source", source).WithValue("provider", sourceCfg.Provider).Info("analytics reporter configured for proxy source")
		reporters[source] = r
	}

	return NewMultiSourceEventReporter(reporters, opts...), nil
}

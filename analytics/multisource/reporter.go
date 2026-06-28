package multisource

import (
	"context"
	"maps"
	"sync"

	"github.com/primandproper/platform-go/analytics"
	"github.com/primandproper/platform-go/analytics/noop"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

const (
	name = "multisource_event_reporter"
	// SourcePropertyKey is the event property used to identify the analytics source (e.g. ios, web).
	// For PostHog, where a single API key is shared across sources, this property distinguishes events.
	SourcePropertyKey = "source"
)

// MultiSourceEventReporter delegates events to per-source EventReporters.
type MultiSourceEventReporter struct {
	o11y      observability.Observer
	reporters map[string]analytics.EventReporter
	mu        sync.RWMutex
}

// NewMultiSourceEventReporter returns a new MultiSourceEventReporter.
func NewMultiSourceEventReporter(
	reporters map[string]analytics.EventReporter,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
) *MultiSourceEventReporter {
	if reporters == nil {
		reporters = make(map[string]analytics.EventReporter)
	}
	return &MultiSourceEventReporter{
		reporters: reporters,
		o11y:      observability.NewObserver(name, logger, tracerProvider),
	}
}

// getReporter returns the reporter for the source, or Noop if unknown/missing.
func (m *MultiSourceEventReporter) getReporter(source string) analytics.EventReporter {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if r, ok := m.reporters[source]; ok && r != nil {
		return r
	}
	m.o11y.Logger().WithValue("source", source).WithValue("known_sources", m.knownSources()).Info("no analytics reporter configured for source, using noop")
	return noop.NewEventReporter()
}

func (m *MultiSourceEventReporter) knownSources() []string {
	sources := make([]string, 0, len(m.reporters))
	for k := range m.reporters {
		sources = append(sources, k)
	}
	return sources
}

// withSourceProperty returns a copy of properties with the source property set.
// For PostHog (single API key across sources), the source property distinguishes events.
func withSourceProperty(source string, properties map[string]any) map[string]any {
	merged := make(map[string]any, len(properties)+1)
	maps.Copy(merged, properties)
	merged[SourcePropertyKey] = source
	return merged
}

// TrackEvent records an event for an identified user.
func (m *MultiSourceEventReporter) TrackEvent(ctx context.Context, source, event, userID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("source", source).Set("event", event).Set("user_id", userID).Set(keys.LengthKey, len(properties))

	return m.getReporter(source).EventOccurred(ctx, event, userID, withSourceProperty(source, properties))
}

// TrackAnonymousEvent records an event for an anonymous user.
func (m *MultiSourceEventReporter) TrackAnonymousEvent(ctx context.Context, source, event, anonymousID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("source", source).Set("event", event).Set("anonymous_id", anonymousID).Set(keys.LengthKey, len(properties))

	return m.getReporter(source).EventOccurredAnonymous(ctx, event, anonymousID, withSourceProperty(source, properties))
}

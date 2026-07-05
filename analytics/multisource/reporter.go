package multisource

import (
	"context"
	"maps"

	"github.com/primandproper/platform-go/v4/analytics"
	"github.com/primandproper/platform-go/v4/analytics/noop"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"
)

const (
	name = "multisource_event_reporter"
	// SourcePropertyKey is the event property used to identify the analytics source (e.g. ios, web).
	// For PostHog, where a single API key is shared across sources, this property distinguishes events.
	SourcePropertyKey = "source"
)

// MultiSourceEventReporter delegates events to per-source EventReporters. The reporters map is
// populated at construction and never mutated afterwards, so reads need no synchronization.
type MultiSourceEventReporter struct {
	o11y      observability.Observer
	reporters map[string]analytics.EventReporter
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

// Close flushes and closes every underlying reporter. Reporters shared across multiple sources
// (e.g. PostHog sources with the same API key) are closed exactly once.
func (m *MultiSourceEventReporter) Close() {
	seen := make(map[analytics.EventReporter]struct{}, len(m.reporters))
	for _, r := range m.reporters {
		if r == nil {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		r.Close()
	}
}

// Shutdown implements do.Shutdowner so the DI container flushes buffered events on shutdown.
func (m *MultiSourceEventReporter) Shutdown() {
	m.Close()
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

// AddUser identifies a user against the reporter for the given source, forwarding the
// user's traits. Every underlying reporter supports identify via analytics.EventReporter.AddUser.
func (m *MultiSourceEventReporter) AddUser(ctx context.Context, source, userID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("source", source).Set("user_id", userID).Set(keys.LengthKey, len(properties))

	return m.getReporter(source).AddUser(ctx, userID, withSourceProperty(source, properties))
}

// TrackAnonymousEvent records an event for an anonymous user.
func (m *MultiSourceEventReporter) TrackAnonymousEvent(ctx context.Context, source, event, anonymousID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	op.Set("source", source).Set("event", event).Set("anonymous_id", anonymousID).Set(keys.LengthKey, len(properties))

	return m.getReporter(source).EventOccurredAnonymous(ctx, event, anonymousID, withSourceProperty(source, properties))
}

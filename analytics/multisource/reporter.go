package multisource

import (
	"context"
	"maps"
	"slices"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
)

const (
	name = "multisource_event_reporter"
	// SourcePropertyKey is the event property used to identify the analytics source (e.g. ios, web).
	// For PostHog, where a single API key is shared across sources, this property distinguishes events.
	SourcePropertyKey = "source"
)

// ErrUnknownSource is returned when an event names a source this reporter was
// not built with.
//
// It is an error rather than a noop substitution. The substitution was
// permanent — every event for that source was dropped for the life of the
// process, and every call returned nil — which is exactly what
// NewMultiSourceEventReporterFromConfig refuses at construction. Nothing about
// dispatch makes the same mistake more forgivable.
var ErrUnknownSource = errors.New("no analytics reporter configured for source")

// MultiSourceEventReporter delegates events to per-source EventReporters. The reporters map is
// populated at construction and never mutated afterwards, so reads need no synchronization.
type MultiSourceEventReporter struct {
	o11y      observability.Observer
	reporters map[string]analytics.EventReporter
}

// NewMultiSourceEventReporter returns a new MultiSourceEventReporter.
func NewMultiSourceEventReporter(reporters map[string]analytics.EventReporter, opts ...Option) *MultiSourceEventReporter {
	o := newOptions(opts)

	if reporters == nil {
		reporters = make(map[string]analytics.EventReporter)
	}
	return &MultiSourceEventReporter{
		reporters: reporters,
		o11y:      observability.NewObserver(name, o.logger, o.tracerProvider),
	}
}

// getReporter returns the reporter for the source, or ErrUnknownSource.
//
// The known sources travel with the error because the caller's next question is
// always "then what did I configure?", and a source name is a deployment
// string nobody can look up from the message alone.
func (m *MultiSourceEventReporter) getReporter(source string) (analytics.EventReporter, error) {
	if r, ok := m.reporters[source]; ok && r != nil {
		return r, nil
	}

	return nil, errors.Wrapf(ErrUnknownSource, "source %q, configured sources %v", source, m.knownSources())
}

// knownSources returns the configured source names, sorted so the same set
// reads the same way every time it appears in an error or a log line.
func (m *MultiSourceEventReporter) knownSources() []string {
	return slices.Sorted(maps.Keys(m.reporters))
}

// Close flushes and closes every underlying reporter. Reporters shared across multiple sources
// (e.g. PostHog sources with the same API key) are closed exactly once.
func (m *MultiSourceEventReporter) Close(ctx context.Context) error {
	// Every reporter is closed even if an earlier one fails, and the failures
	// are joined: one source's flush failing is no reason to leak the rest.
	var errs []error

	seen := make(map[analytics.EventReporter]struct{}, len(m.reporters))
	for source, r := range m.reporters {
		if r == nil {
			continue
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}

		if err := r.Close(ctx); err != nil {
			errs = append(errs, errors.Wrapf(err, "closing reporter for source %q", source))
		}
	}

	return errors.Join(errs...)
}

// Shutdown implements do.Shutdowner so the DI container flushes buffered events
// on shutdown, and reports a failed final flush rather than swallowing it.
func (m *MultiSourceEventReporter) Shutdown(ctx context.Context) error {
	return m.Close(ctx)
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
	ctx, op := m.o11y.Begin(ctx,
		observability.WithValue("source", source),
		observability.WithValue("event", event),
		observability.WithValue("user_id", userID),
		observability.WithValue(keys.LengthKey, len(properties)),
	)
	defer op.End()

	r, err := m.getReporter(source)
	if err != nil {
		return observability.PrepareError(err, op.Span(), "resolving analytics reporter")
	}

	return r.EventOccurred(ctx, event, userID, withSourceProperty(source, properties))
}

// AddUser identifies a user against the reporter for the given source, forwarding the
// user's traits. Every underlying reporter supports identify via analytics.EventReporter.AddUser.
func (m *MultiSourceEventReporter) AddUser(ctx context.Context, source, userID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx,
		observability.WithValue("source", source),
		observability.WithValue("user_id", userID),
		observability.WithValue(keys.LengthKey, len(properties)),
	)
	defer op.End()

	r, err := m.getReporter(source)
	if err != nil {
		return observability.PrepareError(err, op.Span(), "resolving analytics reporter")
	}

	return r.AddUser(ctx, userID, withSourceProperty(source, properties))
}

// TrackAnonymousEvent records an event for an anonymous user.
func (m *MultiSourceEventReporter) TrackAnonymousEvent(ctx context.Context, source, event, anonymousID string, properties map[string]any) error {
	ctx, op := m.o11y.Begin(ctx,
		observability.WithValue("source", source),
		observability.WithValue("event", event),
		observability.WithValue("anonymous_id", anonymousID),
		observability.WithValue(keys.LengthKey, len(properties)),
	)
	defer op.End()

	r, err := m.getReporter(source)
	if err != nil {
		return observability.PrepareError(err, op.Span(), "resolving analytics reporter")
	}

	return r.EventOccurredAnonymous(ctx, event, anonymousID, withSourceProperty(source, properties))
}

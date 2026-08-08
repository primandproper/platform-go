package audit

import (
	"github.com/primandproper/platform-go/v10/clock"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	"github.com/primandproper/platform-go/v10/observability/tracing"
)

// ReaderOption configures a Reader.
type ReaderOption func(*reader)

// WithReaderTablePrefix overrides DefaultTablePrefix. It must match the prefix
// the Recorder writes with.
func WithReaderTablePrefix(prefix string) ReaderOption {
	return func(r *reader) {
		if prefix != "" {
			r.prefix = prefix
		}
	}
}

// WithReaderLogger attaches a logger.
func WithReaderLogger(logger logging.Logger) ReaderOption {
	return func(r *reader) {
		r.logger = logger
	}
}

// WithReaderTracerProvider attaches a tracer provider.
func WithReaderTracerProvider(tracerProvider tracing.Provider) ReaderOption {
	return func(r *reader) {
		r.tracerProvider = tracerProvider
	}
}

// WithReaderMetricsProvider attaches a metrics provider, enabling
// audit_verifications and audit_chain_breaks.
//
// Alert on audit_chain_breaks. Everything else this package emits describes
// throughput; that one says the log is no longer evidence, and it is the only
// instrument here whose non-zero value is an incident on its own.
func WithReaderMetricsProvider(metricsProvider metrics.Provider) ReaderOption {
	return func(r *reader) {
		r.metricsProvider = metricsProvider
	}
}

// RecorderOption configures a Recorder.
type RecorderOption func(*recorder)

// WithRecorderTablePrefix overrides DefaultTablePrefix. The prefix must be a
// plain SQL identifier fragment: it is interpolated into the query text, not
// bound as a parameter.
//
// It is also how a second log gets its own tables — pointing an
// EventAccessed-only Recorder at a different prefix keeps read-auditing's write
// volume out of the mutation log's indexes and lets the two carry different
// retention windows.
func WithRecorderTablePrefix(prefix string) RecorderOption {
	return func(r *recorder) {
		if prefix != "" {
			r.prefix = prefix
		}
	}
}

// WithRecorderClock swaps the clock used to stamp RecordedAt.
func WithRecorderClock(c clock.Clock) RecorderOption {
	return func(r *recorder) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithRecorderLogger attaches a logger.
func WithRecorderLogger(logger logging.Logger) RecorderOption {
	return func(r *recorder) {
		r.logger = logger
	}
}

// WithRecorderTracerProvider attaches a tracer provider, so a Record shows up
// as a child of the span that owns the transaction.
func WithRecorderTracerProvider(tracerProvider tracing.Provider) RecorderOption {
	return func(r *recorder) {
		r.tracerProvider = tracerProvider
	}
}

// WithRecorderMetricsProvider attaches a metrics provider, enabling
// audit_entries_recorded and audit_record_latency_ms. The latency matters more
// here than it would elsewhere: Record runs inside somebody's transaction, so
// its cost is lock hold time on the caller's rows, not just its own.
func WithRecorderMetricsProvider(metricsProvider metrics.Provider) RecorderOption {
	return func(r *recorder) {
		r.metricsProvider = metricsProvider
	}
}

// WithRedaction registers what happens to named fields of one resource type
// before they are written. Register the empty resource type to apply a
// Redaction to every resource type; see Redaction for how the two combine.
func WithRedaction(resourceType string, redaction Redaction) RecorderOption {
	return func(r *recorder) {
		if r.redactions == nil {
			r.redactions = map[string]Redaction{}
		}
		r.redactions[resourceType] = r.redactions[resourceType].merge(redaction)
	}
}

// There are no PruneTarget options. It is a value with exported fields, like
// retention.Table, and it carries no observability of its own: the sweep that
// drives it is a retention.Sweeper's, and so are the spans, the logger, and the
// instruments describing it.

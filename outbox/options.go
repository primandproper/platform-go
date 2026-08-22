package outbox

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// RelayOption configures a Relay.
type RelayOption func(*Relay)

// WithRelayClock swaps the clock driving the poll loop, leases, and backoff.
func WithRelayClock(c clock.Clock) RelayOption {
	return func(r *Relay) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithRelayLogger attaches a logger. The relay reports every publish failure
// and every quarantine through it; without one, a queue that has stopped
// draining is visible only in metrics.
func WithRelayLogger(logger logging.Logger) RelayOption {
	return func(r *Relay) {
		r.logger = logger
	}
}

// WithRelayTracerProvider attaches a tracer provider. Cycles that claim nothing
// are not traced — a root span every poll interval is noise.
func WithRelayTracerProvider(tracerProvider tracing.Provider) RelayOption {
	return func(r *Relay) {
		r.tracerProvider = tracerProvider
	}
}

// WithRelayMetricsProvider attaches a metrics provider.
func WithRelayMetricsProvider(metricsProvider metrics.Provider) RelayOption {
	return func(r *Relay) {
		r.metricsProvider = metricsProvider
	}
}

// WithRelayWakeup gives the relay a channel to cycle on, beside its poll
// ticker. A receive means "there may be work now"; the relay runs the same
// claim it would have run on the next tick, so nothing about the durable path
// changes and a wake that never arrives costs only latency.
//
// It is a bare channel because the relay must not learn where the wake came
// from. database/postgres/pgnotify fills it from LISTEN/NOTIFY, which is the
// case this exists for, but the relay stays dialect-generic and a test fills it
// by hand.
//
// The channel should coalesce — capacity one, non-blocking sends, as
// pgnotify.Listener.Signal does. RelayConfig.MinWakeInterval floors the rate
// regardless, so a channel that does not coalesce costs a full receive per
// send but still cannot drive more than one cycle per interval.
//
// The poll ticker is unchanged and remains the backstop. Correctness never
// depends on a wake arriving, which is what makes it safe to build on an
// at-most-once signal.
func WithRelayWakeup(wakeup <-chan struct{}) RelayOption {
	return func(r *Relay) {
		r.wakeup = wakeup
	}
}

// WriterOption configures a Writer.
type WriterOption func(*Writer)

// WithWriterTablePrefix overrides DefaultTablePrefix. The namespace must be a
// plain SQL identifier fragment with no trailing separator: it is interpolated
// into the query text, not bound as a parameter, and it must match the one the
// migrations were rendered with.
func WithWriterTablePrefix(prefix string) WriterOption {
	return func(w *Writer) {
		if prefix != "" {
			w.table = ddl.Qualify(prefix) + "outbox_messages"
		}
	}
}

// WithWriterClock swaps the clock used to stamp created_at and next_attempt.
func WithWriterClock(c clock.Clock) WriterOption {
	return func(w *Writer) {
		if c != nil {
			w.clock = c
		}
	}
}

// WithWriterLogger attaches a logger.
func WithWriterLogger(logger logging.Logger) WriterOption {
	return func(w *Writer) {
		w.logger = logger
	}
}

// WithWriterTracerProvider attaches a tracer provider, so an Enqueue shows up
// as a child of the span that owns the transaction.
func WithWriterTracerProvider(tracerProvider tracing.Provider) WriterOption {
	return func(w *Writer) {
		w.tracerProvider = tracerProvider
	}
}

// WithWriterNotifyChannel makes Enqueue emit a payload-free pg_notify on
// channel as part of the caller's transaction, so a relay listening on it wakes
// at commit rather than on its next poll.
//
// It is off unless set: an unconfigured Writer emits no extra statement at all,
// because turning this on changes the SQL running inside every caller's
// transaction. The channel must be a plain SQL identifier — it is bound as text
// here, but the listener has to render it into a LISTEN, which takes no
// parameters — and Postgres is the only dialect that has it, so a channel set
// on any other is refused by NewWriter rather than silently dropped.
//
// The notification carries no payload, which is what lets Postgres collapse
// every enqueue in one transaction into a single notification.
func WithWriterNotifyChannel(channel string) WriterOption {
	return func(w *Writer) {
		w.notifyChannel = channel
	}
}

// WithWriterSideEffect registers a named derived write, run inside every
// Enqueue on the caller's executor and in registration order.
//
// It moves an obligation off the call sites and onto the wiring: an event every
// write to this outbox owes — the search index event, a webhook dispatch row
// per subscribed endpoint — cannot be forgotten by a repository method that was
// never asked about it. outbox's package documentation draws the line between
// what belongs here and what belongs in the Enqueue call.
//
// name identifies the effect on spans and in the error an effect's failure
// returns. Unlike the other options, this one guards nothing: an unnamed,
// duplicated, or nil registration is refused by NewWriter rather than quietly
// dropped, since a registration that vanishes is the forgotten event again.
func WithWriterSideEffect(name string, effect SideEffect) WriterOption {
	return func(w *Writer) {
		w.sideEffects = append(w.sideEffects, sideEffect{name: name, effect: effect})
	}
}

// WithWriterMetricsProvider attaches a metrics provider, enabling
// outbox_messages_enqueued and outbox_enqueue_fanout. Pair it with the Relay's
// provider: enqueue rate against publish rate is what tells you whether the
// relay is keeping up, and neither number answers that alone.
func WithWriterMetricsProvider(metricsProvider metrics.Provider) WriterOption {
	return func(w *Writer) {
		w.metricsProvider = metricsProvider
	}
}

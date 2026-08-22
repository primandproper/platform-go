package saga

import (
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/idempotency"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// OutboxPublisherOption configures an outbox-backed EventPublisher.
type OutboxPublisherOption func(*OutboxPublisher)

// WithEventTopic overrides DefaultEventTopic.
func WithEventTopic(topic string) OutboxPublisherOption {
	return func(p *OutboxPublisher) {
		if topic != "" {
			p.topic = topic
		}
	}
}

// RunnerOption configures a Runner at construction.
//
// It is deliberately not parameterized on the Runner's T. Nothing here depends
// on it, and Go cannot infer a type argument from a call's result type — so an
// Option[T] would force every call site to spell the state type out by hand,
// WithRunnerClock[OrderState](c), forever.
type RunnerOption func(*runnerOptions)

// runnerOptions accumulates what the options set, so RunnerOption can stay free
// of the Runner's type parameter.
type runnerOptions struct {
	clock           clock.Clock
	publisher       EventPublisher
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// WithRunnerClock swaps the clock stamping instances.
func WithRunnerClock(c clock.Clock) RunnerOption {
	return func(o *runnerOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithRunnerEventPublisher attaches the publisher lifecycle events go through.
//
// Without one, starting a saga is silent: the row exists and a Worker will
// advance it, but nothing downstream is told. Attach one and the started event
// commits in the same transaction as the instance, so a subscriber that sees
// the event can always read the instance.
func WithRunnerEventPublisher(publisher EventPublisher) RunnerOption {
	return func(o *runnerOptions) {
		if publisher != nil {
			o.publisher = publisher
		}
	}
}

// WithRunnerLogger attaches a logger.
func WithRunnerLogger(logger logging.Logger) RunnerOption {
	return func(o *runnerOptions) {
		o.logger = logger
	}
}

// WithRunnerTracerProvider attaches a tracer provider.
func WithRunnerTracerProvider(tracerProvider tracing.Provider) RunnerOption {
	return func(o *runnerOptions) {
		o.tracerProvider = tracerProvider
	}
}

// WithRunnerMetricsProvider attaches a metrics provider.
func WithRunnerMetricsProvider(metricsProvider metrics.Provider) RunnerOption {
	return func(o *runnerOptions) {
		o.metricsProvider = metricsProvider
	}
}

// SQLStoreOption configures a SQL Store.
type SQLStoreOption func(*SQLStore)

// WithTablePrefix overrides DefaultTablePrefix. It must be a plain SQL
// identifier fragment: it is interpolated into the query text, not bound as a
// parameter, and it must match the prefix the migrations were rendered with.
func WithTablePrefix(prefix string) SQLStoreOption {
	return func(s *SQLStore) {
		if prefix != "" {
			s.tables = newTables(prefix)
		}
	}
}

// WithStoreLogger attaches a logger.
func WithStoreLogger(logger logging.Logger) SQLStoreOption {
	return func(s *SQLStore) {
		s.logger = logger
	}
}

// WithStoreTracerProvider attaches a tracer provider.
func WithStoreTracerProvider(tracerProvider tracing.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.tracerProvider = tracerProvider
	}
}

// WithStoreMetricsProvider attaches a metrics provider.
func WithStoreMetricsProvider(metricsProvider metrics.Provider) SQLStoreOption {
	return func(s *SQLStore) {
		s.metricsProvider = metricsProvider
	}
}

// WorkerOption configures a Worker.
type WorkerOption func(*Worker)

// WithWorkerClock swaps the clock driving the poll loop, leases, and backoff.
func WithWorkerClock(c clock.Clock) WorkerOption {
	return func(w *Worker) {
		if c != nil {
			w.clock = c
		}
	}
}

// WithWorkerLogger attaches a logger. A stuck instance is reported through it
// and nowhere else — there is no caller to return it to — so without one the
// only signal that a saga needs a human is a counter.
func WithWorkerLogger(logger logging.Logger) WorkerOption {
	return func(w *Worker) {
		w.logger = logger
	}
}

// WithWorkerTracerProvider attaches a tracer provider. Cycles that claim
// nothing are not traced — a root span every poll interval is noise, and this
// worker polls every second.
func WithWorkerTracerProvider(tracerProvider tracing.Provider) WorkerOption {
	return func(w *Worker) {
		w.tracerProvider = tracerProvider
	}
}

// WithWorkerMetricsProvider attaches a metrics provider.
func WithWorkerMetricsProvider(metricsProvider metrics.Provider) WorkerOption {
	return func(w *Worker) {
		w.metricsProvider = metricsProvider
	}
}

// WithWorkerEventPublisher attaches the publisher lifecycle events go through.
// Pair it with the Runner's, or a subscriber gets a started event and then
// silence.
func WithWorkerEventPublisher(publisher EventPublisher) WorkerOption {
	return func(w *Worker) {
		if publisher != nil {
			w.publisher = publisher
		}
	}
}

// WithWorkerIdempotency supplies the manager that suppresses a re-executed
// step, keyed per (instance, step, phase).
//
// It is worth setting, and it is worth understanding what it does and does not
// buy — see the package documentation's section on exactly-once. Briefly: the
// key is committed with the step's recorded result, so a crash between "the
// step succeeded" and "the row says so" replays instead of re-executing. A
// crash between "the effect landed" and "the idempotency store committed" still
// re-executes, and no library that does not share a transaction with the step
// can close that window.
//
// Without a manager, every step runs again on every resumption. That is correct
// for a step that only reads or that writes an idempotent upsert, and it is a
// duplicate charge for anything else.
func WithWorkerIdempotency(manager *idempotency.Manager[StepResult]) WorkerOption {
	return func(w *Worker) {
		if manager != nil {
			w.idempotency = manager
		}
	}
}

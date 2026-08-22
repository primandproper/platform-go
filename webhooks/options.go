package webhooks

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// DispatcherOption configures a Dispatcher.
type DispatcherOption func(*StoreDispatcher)

// WithCatalog supplies the set of event types the application publishes.
//
// Without it every event type is unknown and both Register and Dispatch reject
// everything, which is deliberate: a catalog-free dispatcher would accept
// subscriptions to typo'd event types that then never fire, and diagnosing that
// means noticing an absence.
func WithCatalog(catalog Catalog) DispatcherOption {
	return func(d *StoreDispatcher) {
		if catalog != nil {
			d.catalog = catalog
		}
	}
}

// WithDispatcherURLChecker replaces the URL policy Register enforces.
//
// Pair it with the Worker's WithWorkerURLChecker: an endpoint accepted at
// registration and refused at delivery sits in the backlog until it dies, so
// the two halves must agree. See URLChecker for what replacing it costs.
func WithDispatcherURLChecker(checker URLChecker) DispatcherOption {
	return func(d *StoreDispatcher) {
		if checker != nil {
			d.checkURL = checker
		}
	}
}

// WithDispatcherClock swaps the clock stamping deliveries.
func WithDispatcherClock(c clock.Clock) DispatcherOption {
	return func(d *StoreDispatcher) {
		if c != nil {
			d.clock = c
		}
	}
}

// WithDispatcherLogger attaches a logger.
func WithDispatcherLogger(logger logging.Logger) DispatcherOption {
	return func(d *StoreDispatcher) {
		d.logger = logger
	}
}

// WithDispatcherTracerProvider attaches a tracer provider, so a Dispatch shows
// up as a child of the span that owns the transaction.
func WithDispatcherTracerProvider(tracerProvider tracing.Provider) DispatcherOption {
	return func(d *StoreDispatcher) {
		d.tracerProvider = tracerProvider
	}
}

// WithDispatcherMetricsProvider attaches a metrics provider. Pair it with the
// Worker's: dispatch rate against delivery rate is what says whether the worker
// is keeping up, and neither number answers that alone.
func WithDispatcherMetricsProvider(metricsProvider metrics.Provider) DispatcherOption {
	return func(d *StoreDispatcher) {
		d.metricsProvider = metricsProvider
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

// WithStoreClock swaps the clock stamping endpoint updates and archivals. The
// dispatcher and worker take one already; this is the third of the three, so a
// deployment that injects time injects all of it.
func WithStoreClock(c clock.Clock) SQLStoreOption {
	return func(s *SQLStore) {
		if c != nil {
			s.clock = c
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
//
// Worth setting. The store's spans are where a slow dispatch turns out to be a
// slow claim, which is otherwise a gap inside the worker's own span.
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

// WithWorkerLogger attaches a logger. The worker reports every delivery failure
// and every dead dispatch through it; without one, a subscriber that has stopped
// accepting deliveries is visible only in metrics.
func WithWorkerLogger(logger logging.Logger) WorkerOption {
	return func(w *Worker) {
		w.logger = logger
	}
}

// WithWorkerTracerProvider attaches a tracer provider. Cycles that claim
// nothing are not traced — a root span every poll interval is noise.
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

// WithHTTPClient supplies the client every delivery goes through.
//
// One client for the whole worker is the point. A client built per delivery —
// which is what this package was extracted to replace — reuses no connections,
// so every delivery pays a fresh TCP handshake and a fresh TLS handshake to a
// subscriber it just talked to.
//
// The supplied client's redirect policy is overridden: following a redirect
// would deliver a signed payload to a host the operator never registered and
// never had checked, which turns an open redirect on a subscriber's domain into
// an SSRF. Its transport is left alone.
func WithHTTPClient(client *http.Client) WorkerOption {
	return func(w *Worker) {
		if client != nil {
			w.client = client
		}
	}
}

// WithWorkerURLChecker replaces the URL policy re-checked at delivery.
//
// Pair it with the Dispatcher's WithDispatcherURLChecker: an endpoint accepted
// at registration and refused here sits in the backlog until it dies, so the
// two halves must agree. See URLChecker for what replacing it costs.
func WithWorkerURLChecker(checker URLChecker) WorkerOption {
	return func(w *Worker) {
		if checker != nil {
			w.checkURL = checker
		}
	}
}

// WithCircuitBreakerFactory supplies the per-endpoint circuit breakers.
//
// Without it every endpoint gets a noop breaker and a permanently dead
// subscriber is retried at full rate forever, competing with healthy endpoints
// for the same worker pool.
func WithCircuitBreakerFactory(factory CircuitBreakerFactory) WorkerOption {
	return func(w *Worker) {
		if factory != nil {
			w.breaker = factory
		}
	}
}

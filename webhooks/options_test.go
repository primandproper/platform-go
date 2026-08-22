package webhooks

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Every option here follows the same contract: a usable value is applied, and a
// zero value leaves the existing setting alone rather than clearing it. The
// second half is the part worth testing — an option that nils out a dependency
// when handed a nil turns "I did not configure this" into a panic at the first
// delivery.

func TestDispatcherOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithCatalog", func(t *testing.T) {
		t.Parallel()

		d := &StoreDispatcher{catalog: Catalog{}}

		WithCatalog(testCatalog)(d)
		test.True(t, d.catalog.Known("order.created"))

		// A nil catalog would silently make every event unknown.
		WithCatalog(nil)(d)
		test.True(t, d.catalog.Known("order.created"))
	})

	T.Run("WithDispatcherClock", func(t *testing.T) {
		t.Parallel()

		original := clock.NewClock()
		d := &StoreDispatcher{clock: original}

		replacement := clock.NewClock()
		WithDispatcherClock(replacement)(d)
		test.True(t, d.clock == replacement)

		WithDispatcherClock(nil)(d)
		test.True(t, d.clock == replacement)
	})

	T.Run("WithDispatcherURLChecker", func(t *testing.T) {
		t.Parallel()

		d := &StoreDispatcher{checkURL: CheckEndpointURL}

		WithDispatcherURLChecker(allowAnyURL)(d)
		test.NoError(t, d.checkURL(t.Context(), "http://127.0.0.1/hooks"))

		// Nilling the checker would remove the SSRF guard entirely.
		WithDispatcherURLChecker(nil)(d)
		test.NoError(t, d.checkURL(t.Context(), "http://127.0.0.1/hooks"))
	})

	T.Run("WithDispatcherLogger", func(t *testing.T) {
		t.Parallel()

		d := &StoreDispatcher{}

		logger := logging.EnsureLogger(nil)
		WithDispatcherLogger(logger)(d)
		test.NotNil(t, d.logger)
	})

	T.Run("WithDispatcherTracerProvider", func(t *testing.T) {
		t.Parallel()

		d := &StoreDispatcher{}

		WithDispatcherTracerProvider(tracingnoop.NewTracerProvider())(d)
		test.NotNil(t, d.tracerProvider)
	})

	T.Run("WithDispatcherMetricsProvider", func(t *testing.T) {
		t.Parallel()

		d := &StoreDispatcher{}

		WithDispatcherMetricsProvider(metrics.EnsureMetricsProvider(nil))(d)
		test.NotNil(t, d.metricsProvider)
	})
}

func TestWorkerOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithWorkerClock", func(t *testing.T) {
		t.Parallel()

		replacement := clock.NewClock()
		w := &Worker{clock: clock.NewClock()}

		WithWorkerClock(replacement)(w)
		test.True(t, w.clock == replacement)

		WithWorkerClock(nil)(w)
		test.True(t, w.clock == replacement)
	})

	T.Run("WithWorkerLogger", func(t *testing.T) {
		t.Parallel()

		w := &Worker{}

		WithWorkerLogger(logging.EnsureLogger(nil))(w)
		test.NotNil(t, w.logger)
	})

	T.Run("WithWorkerTracerProvider", func(t *testing.T) {
		t.Parallel()

		w := &Worker{}

		WithWorkerTracerProvider(tracingnoop.NewTracerProvider())(w)
		test.NotNil(t, w.tracerProvider)
	})

	T.Run("WithWorkerMetricsProvider", func(t *testing.T) {
		t.Parallel()

		w := &Worker{}

		WithWorkerMetricsProvider(metrics.EnsureMetricsProvider(nil))(w)
		test.NotNil(t, w.metricsProvider)
	})

	T.Run("WithHTTPClient", func(t *testing.T) {
		t.Parallel()

		replacement := &http.Client{Timeout: time.Second}
		w := &Worker{}

		WithHTTPClient(replacement)(w)
		test.True(t, w.client == replacement)

		// A nil client would leave the worker with nothing to deliver through.
		WithHTTPClient(nil)(w)
		test.True(t, w.client == replacement)
	})

	T.Run("WithWorkerURLChecker", func(t *testing.T) {
		t.Parallel()

		w := &Worker{checkURL: CheckEndpointURL}

		WithWorkerURLChecker(allowAnyURL)(w)
		test.NoError(t, w.checkURL(t.Context(), "http://127.0.0.1/hooks"))

		WithWorkerURLChecker(nil)(w)
		test.NoError(t, w.checkURL(t.Context(), "http://127.0.0.1/hooks"))
	})

	T.Run("WithCircuitBreakerFactory", func(t *testing.T) {
		t.Parallel()

		called := false
		factory := func(string) (circuitbreaking.CircuitBreaker, error) {
			called = true

			return cbnoop.NewCircuitBreaker(), nil
		}

		w := &Worker{breakers: map[string]circuitbreaking.CircuitBreaker{}}

		WithCircuitBreakerFactory(factory)(w)
		must.NotNil(t, w.breaker)

		_, err := w.breakerFor("endpoint-1")
		must.NoError(t, err)
		test.True(t, called)

		WithCircuitBreakerFactory(nil)(w)
		test.NotNil(t, w.breaker)
	})

	// A factory returning a nil breaker would panic at the first delivery, so
	// the resolver substitutes a noop rather than trusting it.
	T.Run("a factory returning nil yields a usable breaker", func(t *testing.T) {
		t.Parallel()

		w := &Worker{
			breakers: map[string]circuitbreaking.CircuitBreaker{},
			breaker: func(string) (circuitbreaking.CircuitBreaker, error) {
				return nil, nil
			},
		}

		breaker, err := w.breakerFor("endpoint-1")
		must.NoError(t, err)
		must.NotNil(t, breaker)
		test.True(t, breaker.CanProceed())
	})
}

func TestSQLStoreOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithTablePrefix", func(t *testing.T) {
		t.Parallel()

		s := &SQLStore{tables: newTables(DefaultTablePrefix)}

		WithTablePrefix("acme_hook")(s)
		test.EqOp(t, "acme_hook_webhooks_dispatches", s.tables.dispatches)

		// An empty prefix would render tables named "_dispatches".
		WithTablePrefix("")(s)
		test.EqOp(t, "acme_hook_webhooks_dispatches", s.tables.dispatches)
	})
}

// A nil option in the variadic list is skipped rather than dereferenced, so a
// caller building an option slice conditionally cannot panic the constructor.
func TestNilOptionsAreSkipped(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var (
			absentDispatcher DispatcherOption
			absentWorker     WorkerOption
			absentStore      SQLStoreOption
		)

		_, err := NewDispatcher(&fakeStore{}, absentDispatcher)
		test.NoError(t, err)

		_, err = NewWorker(t.Context(), &WorkerConfig{}, &fakeStore{}, absentWorker)
		test.NoError(t, err)

		_, err = NewSQLStore(newSQLiteEnv(t).client, absentStore)
		test.NoError(t, err)
	})
}

// allowAnyURLCtx documents that the test helper matches the URLChecker shape.
var _ URLChecker = func(context.Context, string) error { return nil }

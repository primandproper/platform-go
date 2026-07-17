package httprouter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/routing"

	"github.com/shoenig/test"
)

func newTestBackend(t *testing.T, cfg *Config) routing.Backend {
	t.Helper()

	return NewBackend(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), metricsnoop.NewMetricsProvider(), cfg)
}

func TestNewBackend(T *testing.T) {
	T.Parallel()

	T.Run("returns a usable backend", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})
		test.NotNil(t, b)
		test.NotNil(t, b.Handler())
	})
}

func TestBackend_HandleAndPathValue(T *testing.T) {
	T.Parallel()

	T.Run("registers a route and resolves {name} path values via :name", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})

		var gotID string
		b.Handle(http.MethodGet, "/things/{id}", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotID = b.PathValue(r, "id")
			w.WriteHeader(http.StatusNoContent)
		}))

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things/42", http.NoBody))

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.EqOp(t, "42", gotID)
	})

	T.Run("method scoping rejects the wrong verb", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})

		b.Handle(http.MethodGet, "/only-get", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/only-get", http.NoBody))

		test.EqOp(t, http.StatusMethodNotAllowed, rec.Code)
	})
}

func TestBackend_Use(T *testing.T) {
	T.Parallel()

	T.Run("applies middleware and drops nils", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})

		b.Use(nil, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Middleware", "on")
				next.ServeHTTP(w, r)
			})
		})

		b.Handle(http.MethodGet, "/u", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/u", http.NoBody))

		test.EqOp(t, http.StatusOK, rec.Code)
		test.EqOp(t, "on", rec.Header().Get("X-Middleware"))
	})
}

func TestBackend_Recovery(T *testing.T) {
	T.Parallel()

	T.Run("panicking handler yields a 500, not a severed connection", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})
		b.Handle(http.MethodGet, "/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		}))

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody))

		test.EqOp(t, http.StatusInternalServerError, rec.Code)
	})
}

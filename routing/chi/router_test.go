package chi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v5/observability"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
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

	T.Run("registers a route and resolves path values", func(t *testing.T) {
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
}

func TestBackend_Use(T *testing.T) {
	T.Parallel()

	T.Run("applies middleware and drops nils", func(t *testing.T) {
		t.Parallel()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})

		// nil is filtered by convertMiddleware; the real middleware sets a header.
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

func Test_buildChiMux_CORS(T *testing.T) {
	T.Parallel()

	mux := buildChiMux(
		observability.NewObserverForTest(T.Name()),
		metricsnoop.NewMetricsProvider(),
		&Config{ValidDomains: []string{"example.com"}, EnableCORSForLocalhost: true},
	)
	mux.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	allowOrigin := func(t *testing.T, origin string) string {
		t.Helper()

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody)
		must.NoError(t, err)
		req.Header.Set("Origin", origin)

		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		return rec.Header().Get("Access-Control-Allow-Origin")
	}

	cases := []struct {
		name   string
		origin string
		want   string
	}{
		{name: "https on allowed host is echoed", origin: "https://example.com", want: "https://example.com"},
		{name: "http on allowed host is rejected", origin: "http://example.com", want: ""},
		{name: "http localhost is allowed", origin: "http://localhost", want: "http://localhost"},
		{name: "disallowed host is rejected", origin: "https://evil.example", want: ""},
		{name: "unparseable origin is rejected", origin: "http://\x7f", want: ""},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.want, allowOrigin(t, tc.origin))
		})
	}
}

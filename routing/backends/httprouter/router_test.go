package httprouter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
)

func newTestBackend(t *testing.T, cfg *Config) routing.Backend {
	t.Helper()

	return NewBackend(cfg)
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

// TestBackend_EscapedPathDispatchLeavesTheMissPathAlone covers the half of
// escapedPathDispatch that is easy to break: when the escaped path matches
// nothing, httprouter must still see the request exactly as it always did, so
// its trailing-slash redirect, 405, and OPTIONS handling — which the shared
// middleware stack deliberately leaves to it — keep working.
func TestBackend_EscapedPathDispatchLeavesTheMissPathAlone(T *testing.T) {
	T.Parallel()

	serve := func(t *testing.T, method, target string) *httptest.ResponseRecorder {
		t.Helper()

		b := newTestBackend(t, &Config{ServiceName: t.Name()})
		b.Handle(http.MethodGet, "/things/{slug}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}))

		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, http.NoBody))

		return rec
	}

	cases := []struct {
		name     string
		method   string
		target   string
		location string
		expected int
	}{
		{
			name: "trailing slash still redirects", method: http.MethodGet, target: "/things/plain/",
			expected: http.StatusMovedPermanently, location: "/things/plain",
		},
		{
			// The redirect is built from the request httprouter was given, which
			// escapedPathDispatch never touched, so the Location is escaped once
			// and not twice.
			name: "trailing slash still redirects on an escaped path", method: http.MethodGet, target: "/things/a%2Fb/",
			expected: http.StatusMovedPermanently, location: "/things/a%2Fb",
		},
		{name: "wrong verb is still a 405", method: http.MethodPost, target: "/things/plain", expected: http.StatusMethodNotAllowed},
		{name: "wrong verb is still a 405 on an escaped path", method: http.MethodPost, target: "/things/a%2Fb", expected: http.StatusMethodNotAllowed},
		{name: "an unregistered path is still a 404", method: http.MethodGet, target: "/nope/a%2Fb", expected: http.StatusNotFound},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := serve(t, tc.method, tc.target)

			test.EqOp(t, tc.expected, rec.Code)
			if tc.location != "" {
				test.EqOp(t, tc.location, rec.Header().Get("Location"))
			}
		})
	}
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

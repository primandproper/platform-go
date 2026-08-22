package oauth2server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newTestRouter builds a router for the mount cases.
func newTestRouter(t *testing.T) *routing.Router {
	t.Helper()

	return routing.New(
		chi.NewBackend(&chi.Config{ServiceName: "oauth2server-test"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
	)
}

func TestServer_Mount(T *testing.T) {
	T.Parallel()

	T.Run("registers every endpoint at the path its RFC fixes", func(t *testing.T) {
		t.Parallel()

		router := newTestRouter(t)

		server, err := oauth2server.NewServer(testIssuer, memory.NewStore(), &passwordAuthenticator{})
		must.NoError(t, err)

		server.Mount(router)
		must.NoError(t, router.Err())

		mounted := httptest.NewServer(router.Handler())
		t.Cleanup(mounted.Close)

		// Both /authorize methods, because the GET renders the form and the POST
		// signs in — a Mount that registered only one would leave a login form
		// that posts to a 405.
		for _, tc := range []struct {
			method string
			path   string
			status int
		}{
			{http.MethodGet, oauth2server.PathAuthorizationServerMetadata, http.StatusOK},
			{http.MethodGet, oauth2server.PathAuthorize, http.StatusBadRequest},
			{http.MethodPost, oauth2server.PathAuthorize, http.StatusBadRequest},
			{http.MethodPost, oauth2server.PathToken, http.StatusUnauthorized},
			{http.MethodPost, oauth2server.PathRegister, http.StatusBadRequest},
			{http.MethodPost, oauth2server.PathRevoke, http.StatusUnauthorized},
		} {
			req, reqErr := http.NewRequestWithContext(t.Context(), tc.method, mounted.URL+tc.path, http.NoBody)
			must.NoError(t, reqErr)

			res, doErr := mounted.Client().Do(req)
			must.NoError(t, doErr)
			must.NoError(t, res.Body.Close())

			// The status is the endpoint's own answer to an empty request rather
			// than a 404 or a 405, which is what says the route is this handler.
			test.EqOp(t, tc.status, res.StatusCode)
		}
	})

	T.Run("leaves the registration route unmounted when registration is not served", func(t *testing.T) {
		t.Parallel()

		router := newTestRouter(t)

		server, err := oauth2server.NewServer(testIssuer, memory.NewStore(), &passwordAuthenticator{},
			oauth2server.WithDynamicRegistration(false))
		must.NoError(t, err)

		server.Mount(router)
		must.NoError(t, router.Err())

		mounted := httptest.NewServer(router.Handler())
		t.Cleanup(mounted.Close)

		for _, tc := range []struct {
			path   string
			status int
		}{
			// Not routed at all, which is what the discovery document now says
			// by leaving registration_endpoint out.
			{oauth2server.PathRegister, http.StatusNotFound},

			// The other five untouched: this switch turns off one endpoint, not
			// the server.
			{oauth2server.PathRevoke, http.StatusUnauthorized},
		} {
			req, reqErr := http.NewRequestWithContext(t.Context(), http.MethodPost, mounted.URL+tc.path, http.NoBody)
			must.NoError(t, reqErr)

			res, doErr := mounted.Client().Do(req)
			must.NoError(t, doErr)
			must.NoError(t, res.Body.Close())

			test.EqOp(t, tc.status, res.StatusCode)
		}
	})

	T.Run("runs the middleware it was given", func(t *testing.T) {
		t.Parallel()

		router := newTestRouter(t)

		server, err := oauth2server.NewServer(testIssuer, memory.NewStore(), &passwordAuthenticator{})
		must.NoError(t, err)

		// The slot a deployment puts a rate limiter in front of /register in,
		// which is the one endpoint here an anonymous caller writes rows
		// through.
		server.Mount(router, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
				res.Header().Set("X-Limited", "yes")
				next.ServeHTTP(res, req)
			})
		})
		must.NoError(t, router.Err())

		mounted := httptest.NewServer(router.Handler())
		t.Cleanup(mounted.Close)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			mounted.URL+oauth2server.PathAuthorizationServerMetadata, http.NoBody)
		must.NoError(t, err)

		res, err := mounted.Client().Do(req)
		must.NoError(t, err)
		must.NoError(t, res.Body.Close())

		test.EqOp(t, "yes", res.Header.Get("X-Limited"))
	})
}

func TestResourceMetadata_Mount(T *testing.T) {
	T.Parallel()

	T.Run("registers the document at the well-known path", func(t *testing.T) {
		t.Parallel()

		router := newTestRouter(t)

		doc, err := oauth2server.NewResourceMetadata(testResource, []string{testIssuer})
		must.NoError(t, err)

		doc.Mount(router)
		must.NoError(t, router.Err())

		mounted := httptest.NewServer(router.Handler())
		t.Cleanup(mounted.Close)

		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
			mounted.URL+oauth2server.PathProtectedResourceMetadata, http.NoBody)
		must.NoError(t, err)

		res, err := mounted.Client().Do(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, http.StatusOK, res.StatusCode)
		test.StrContains(t, readBody(t, res), testResource)
	})
}

// failingMetricsProvider serves the noop provider's instruments for every name
// except failOn, which reports an error. It walks NewServer's instrument setup
// one failure at a time.
func failingMetricsProvider(failOn string) metrics.Provider {
	base := metricsnoop.NewMetricsProvider()
	boom := platformerrors.New("instrument unavailable")

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Counter(name, opts...)
		},
		NewFloat64HistogramFunc: func(name string, opts ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewFloat64Histogram(name, opts...)
		},
	}
}

func TestNewServer_instrumentFailures(T *testing.T) {
	T.Parallel()

	// Every instrument NewServer creates, with the message it wraps the failure
	// in. An instrument added without a matching error path shows up here as a
	// construction that unexpectedly succeeds.
	for _, tc := range []struct {
		instrument string
		wantErr    string
	}{
		{"requests", "creating oauth2server request counter"},
		{"errors", "creating oauth2server error counter"},
		{"latency_ms", "creating oauth2server latency histogram"},
		{"codes_issued", "creating authorization codes issued counter"},
		{"tokens_issued", "creating tokens issued counter"},
		{"clients_registered", "creating clients registered counter"},
		{"refresh_reuse_detected", "creating refresh reuse counter"},
		{"revocations", "creating revocations counter"},
	} {
		T.Run(tc.instrument, func(t *testing.T) {
			t.Parallel()

			server, err := oauth2server.NewServer(testIssuer, memory.NewStore(), &passwordAuthenticator{},
				oauth2server.WithMetricsProvider(failingMetricsProvider(fmt.Sprintf("oauth2server_%s", tc.instrument))))

			test.Nil(t, server)
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

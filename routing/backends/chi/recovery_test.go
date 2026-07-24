package chi

import (
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/logging"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// captureLogger is a logging.Logger test double that records Error calls and the
// requests passed to WithRequest, so tests can assert what a middleware logged and
// in which request context.
type captureLogger struct {
	mu       *sync.Mutex
	errors   *[]error
	requests *[]*http.Request
}

func newCaptureLogger() *captureLogger {
	return &captureLogger{
		mu:       &sync.Mutex{},
		errors:   &[]error{},
		requests: &[]*http.Request{},
	}
}

func (l *captureLogger) Error(_ string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.errors = append(*l.errors, err)
}

func (l *captureLogger) WithRequest(req *http.Request) logging.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.requests = append(*l.requests, req)

	return l
}

func (l *captureLogger) capturedErrors() []error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]error(nil), *l.errors...)
}

func (l *captureLogger) capturedRequests() []*http.Request {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]*http.Request(nil), *l.requests...)
}

func (l *captureLogger) Info(string)                                {}
func (l *captureLogger) Debug(string)                               {}
func (l *captureLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}
func (l *captureLogger) Clone() logging.Logger                      { return l }
func (l *captureLogger) WithName(string) logging.Logger             { return l }
func (l *captureLogger) WithValues(map[string]any) logging.Logger   { return l }
func (l *captureLogger) WithValue(string, any) logging.Logger       { return l }
func (l *captureLogger) WithResponse(*http.Response) logging.Logger { return l }
func (l *captureLogger) WithError(error) logging.Logger             { return l }
func (l *captureLogger) WithSpan(trace.Span) logging.Logger         { return l }

func Test_buildRecoveryMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("panicking handler yields a 500 and a log, not a severed connection", func(t *testing.T) {
		t.Parallel()

		cl := newCaptureLogger()
		obs := observability.NewObserverWithTracer(t.Name(), cl, nil)
		mux := buildChiMux(obs, metricsnoop.NewMetricsProvider(), &Config{})

		mux.Get("/boom", func(http.ResponseWriter, *http.Request) {
			panic("kaboom")
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody)
		res := httptest.NewRecorder()

		mux.ServeHTTP(res, req)

		test.EqOp(t, http.StatusInternalServerError, res.Code)
		must.SliceLen(t, 1, cl.capturedErrors())
	})

	T.Run("re-panics on http.ErrAbortHandler", func(t *testing.T) {
		t.Parallel()

		obs := observability.NewObserverForTest(t.Name())
		handler := buildRecoveryMiddleware(obs)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic(http.ErrAbortHandler)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", http.NoBody)
		res := httptest.NewRecorder()

		defer func() {
			err, ok := recover().(error)
			test.True(t, ok && stderrors.Is(err, http.ErrAbortHandler))
		}()

		handler.ServeHTTP(res, req)
	})
}

func Test_buildChiMux_middlewareOrdering(T *testing.T) {
	T.Parallel()

	T.Run("request ID and real client IP reach the logging middleware", func(t *testing.T) {
		t.Parallel()

		cl := newCaptureLogger()
		obs := observability.NewObserverWithTracer(t.Name(), cl, nil)
		mux := buildChiMux(obs, metricsnoop.NewMetricsProvider(), &Config{})

		mux.Get("/thing", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/thing", http.NoBody)
		req.Header.Set("X-Real-IP", "1.2.3.4")
		res := httptest.NewRecorder()

		mux.ServeHTTP(res, req)

		reqs := cl.capturedRequests()
		must.SliceLen(t, 1, reqs)

		logged := reqs[0]
		// RequestID ran before the logging middleware, so the ID is in context.
		test.NotEq(t, "", chimiddleware.GetReqID(logged.Context()))
		// RealIP ran before the logging middleware, so RemoteAddr is the client IP, not the proxy.
		test.EqOp(t, "1.2.3.4", logged.RemoteAddr)
	})
}

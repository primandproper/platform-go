package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	httpx "github.com/primandproper/platform-go/v13/errors/http"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
)

var errBoom = errors.New("boom")

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithLogger attaches a logger", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead), WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.NotNil(t, e.logger)
	})

	T.Run("WithMetricsProvider attaches counters", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))

		must.NoError(t, err)
		test.NotNil(t, e.checksCounter)
		test.NotNil(t, e.denialsCounter)
		test.NotNil(t, e.noGrantsCounter)
	})

	T.Run("WithAuditOnly records the mode", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead), WithAuditOnly(), WithLogger(loggingnoop.NewLogger()))

		must.NoError(t, err)
		test.True(t, e.auditOnly)
	})

	T.Run("a nil logger is replaced rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(grantsOf(permRead), WithLogger(nil))

		must.NoError(t, err)
		test.NotNil(t, e.logger)
	})
}

func TestNewEnforcer_MetricsFailure(T *testing.T) {
	T.Parallel()

	for _, failOn := range []string{
		serviceName + "_http_checks",
		serviceName + "_http_denials",
		serviceName + "_http_missing_grants",
	} {
		T.Run("surfaces a failure creating "+failOn, func(t *testing.T) {
			t.Parallel()

			mp := &metricsmock.ProviderMock{
				NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
					if name == failOn {
						return nil, errBoom
					}

					return &metricsmock.Int64CounterMock{}, nil
				},
			}

			_, err := NewEnforcer(grantsOf(permRead), WithMetricsProvider(mp))

			test.ErrorIs(t, err, errBoom)
		})
	}
}

// failingResponseWriter refuses every write, which is what a client that
// disconnected mid-response looks like from the handler's side.
type failingResponseWriter struct {
	header http.Header
	status int
}

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = http.Header{}
	}

	return w.header
}

func (w *failingResponseWriter) Write([]byte) (int, error) { return 0, errBoom }

func (w *failingResponseWriter) WriteHeader(status int) { w.status = status }

func TestEnforcer_DenialWriteFailure(T *testing.T) {
	T.Parallel()

	// A denial that cannot be written is logged rather than panicking: the
	// status is already on the wire, and there is nothing further to tell a
	// client that has gone away.
	T.Run("logs rather than panicking when the response cannot be written", func(t *testing.T) {
		t.Parallel()

		e, err := NewEnforcer(noGrants(), WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		handler := e.Require(permRead)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("handler ran for a denied request")
		}))

		res := &failingResponseWriter{}
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/things", http.NoBody))

		test.EqOp(t, http.StatusForbidden, res.status)
	})
}

// The trace ID is what ties a 403 a user reports back to the decision that
// produced it, so the envelope carries it whenever the request is sampled.
func TestEnforcer_DenialCarriesTraceID(T *testing.T) {
	T.Parallel()

	T.Run("includes the trace ID when one is present", func(t *testing.T) {
		t.Parallel()

		traceID, err := oteltrace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
		must.NoError(t, err)

		spanID, err := oteltrace.SpanIDFromHex("00f067aa0ba902b7")
		must.NoError(t, err)

		ctx := oteltrace.ContextWithSpanContext(t.Context(), oteltrace.NewSpanContext(
			oteltrace.SpanContextConfig{
				TraceID:    traceID,
				SpanID:     spanID,
				TraceFlags: oteltrace.FlagsSampled,
			},
		))

		e, err := NewEnforcer(noGrants(), WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)

		handler := e.Require(permRead)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("handler ran for a denied request")
		}))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/things", http.NoBody).WithContext(ctx))

		var body httpx.APIResponse[any]
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))

		test.EqOp(t, "4bf92f3577b34da6a3ce929d0e0e4736", body.Details.TraceID)
	})
}

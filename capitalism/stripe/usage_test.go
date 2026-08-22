package stripe

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/capitalism"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
)

// newTestUsageReporter builds a stripeUsageReporter whose Stripe client talks to
// an httptest server, so a test can drive the post and inspect the request Stripe
// would have received.
func newTestUsageReporter(t *testing.T, status int, body string) (*UsageReporter, *[]capturedRequest) {
	t.Helper()

	var (
		mu       sync.Mutex
		captured []capturedRequest
	)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()

		mu.Lock()
		captured = append(captured, capturedRequest{
			method:         r.Method,
			path:           r.URL.Path,
			form:           r.Form,
			idempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)

	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: new(ts.URL)})
	sc := &client.API{}
	sc.Init("sk_test_123", &stripe.Backends{API: backend, Connect: backend, Uploads: backend})

	instruments, err := newInstruments(metricsnoop.NewMetricsProvider())
	must.NoError(t, err)

	return &UsageReporter{
		client:      sc,
		o11y:        observability.NewObserver(usageImplementationName, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider()),
		instruments: instruments,
	}, &captured
}

func TestNewUsageReporter(T *testing.T) {
	T.Parallel()

	T.Run("builds with an API key", func(t *testing.T) {
		t.Parallel()

		reporter, err := NewUsageReporter(&Config{APIKey: "sk_test_123"},
			WithLogger(loggingnoop.NewLogger()), WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, reporter)
	})

	T.Run("refuses a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewUsageReporter(nil)

		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("requires an API key", func(t *testing.T) {
		t.Parallel()

		// Unlike the payment manager there is no inbound path here, so a reporter
		// without a key could do nothing at all and would fail on its first flush
		// rather than at startup.
		_, err := NewUsageReporter(&Config{WebhookSecret: "whsec_123"})

		test.ErrorIs(t, err, ErrAPIKeyNotConfigured)
	})
}

func TestStripeUsageReporter_ReportUsage(T *testing.T) {
	T.Parallel()

	const body = `{"object":"billing.meter_event","event_name":"api_requests","identifier":"mtr_abc","timestamp":1788220799}`

	T.Run("posts a meter event under the caller's idempotency key", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		must.NoError(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID:     "cus_123",
			MeterName:      "api_requests",
			Quantity:       42,
			IdempotencyKey: "mtr_abc",
			Metadata:       map[string]string{"metering_meter": "api_requests"},
		}))

		must.SliceLen(t, 1, *captured)

		req := (*captured)[0]
		test.EqOp(t, http.MethodPost, req.method)
		test.EqOp(t, "/v1/billing/meter_events", req.path)
		test.EqOp(t, "api_requests", req.form.Get("event_name"))
		test.EqOp(t, "cus_123", req.form.Get("payload[stripe_customer_id]"))
		test.EqOp(t, "42", req.form.Get("payload[value]"))
		// Metadata rides along in the payload, which is the only place a meter
		// event has to carry it.
		test.EqOp(t, "api_requests", req.form.Get("payload[metering_meter]"))
		// Both dedup mechanisms, from the one key. The header replays an identical
		// retry; the identifier covers a retry the flush loop reassembles, over a
		// window of at least 24 hours.
		test.EqOp(t, "mtr_abc", req.form.Get("identifier"))
		test.EqOp(t, "mtr_abc", req.idempotencyKey)
	})

	T.Run("leaves the timestamp to Stripe for a zero event time", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		must.NoError(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID:     "cus_123",
			MeterName:      "api_requests",
			Quantity:       1,
			IdempotencyKey: "mtr_abc",
		}))

		must.SliceLen(t, 1, *captured)

		// Unset rather than a locally computed "now": Stripe refuses an event
		// dated more than five minutes ahead, and a worker whose clock runs fast
		// would otherwise have every flush refused.
		test.EqOp(t, "", (*captured)[0].form.Get("timestamp"))
	})

	T.Run("stamps an explicit event time", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		occurredAt := time.Date(2026, time.August, 31, 23, 59, 59, 0, time.UTC)

		must.NoError(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID:     "cus_123",
			MeterName:      "api_requests",
			Quantity:       1,
			IdempotencyKey: "mtr_abc",
			OccurredAt:     occurredAt,
		}))

		must.SliceLen(t, 1, *captured)
		test.EqOp(t, "1788220799", (*captured)[0].form.Get("timestamp"))
	})

	T.Run("refuses a nil input", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		test.ErrorIs(t, reporter.ReportUsage(t.Context(), nil), platformerrors.ErrNilInputParameter)
		test.SliceEmpty(t, *captured)
	})

	T.Run("refuses an empty customer", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		// Meter events key on a customer, so an empty one is usage belonging to
		// nobody.
		test.ErrorIs(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			MeterName: "api_requests", Quantity: 1, IdempotencyKey: "mtr_abc",
		}), ErrEmptyCustomerID)

		test.SliceEmpty(t, *captured)
	})

	T.Run("refuses an empty meter name", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		// The name is what ties an event to a billing meter; whether it names one
		// that exists is Stripe's to decide, but an absent name is catchable here.
		test.ErrorIs(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID: "cus_123", Quantity: 1, IdempotencyKey: "mtr_abc",
		}), ErrEmptyMeterName)

		test.SliceEmpty(t, *captured)
	})

	T.Run("refuses an empty idempotency key", func(t *testing.T) {
		t.Parallel()

		reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

		// Refused rather than defaulted: a post with no key double-bills on
		// retry, and generating one here would make every attempt distinct, which
		// is precisely the wrong answer.
		test.ErrorIs(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID: "cus_123", MeterName: "api_requests", Quantity: 1,
		}), ErrEmptyUsageIdempotencyKey)

		test.SliceEmpty(t, *captured)
	})

	T.Run("refuses metadata on a reserved payload key", func(t *testing.T) {
		t.Parallel()

		for _, key := range []string{"stripe_customer_id", "value"} {
			t.Run(key, func(t *testing.T) {
				t.Parallel()

				reporter, captured := newTestUsageReporter(t, http.StatusOK, body)

				// Honoring it would bill a different customer, or a different
				// amount; dropping it would lose an annotation the caller thought
				// it stored. Neither is this adapter's call to make.
				test.ErrorIs(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
					CustomerID:     "cus_123",
					MeterName:      "api_requests",
					Quantity:       1,
					IdempotencyKey: "mtr_abc",
					Metadata:       map[string]string{key: "cus_evil"},
				}), ErrReservedPayloadKey)

				test.SliceEmpty(t, *captured)
			})
		}
	})

	T.Run("propagates a provider error", func(t *testing.T) {
		t.Parallel()

		reporter, _ := newTestUsageReporter(t, http.StatusBadRequest,
			`{"error":{"message":"no such meter","type":"invalid_request_error"}}`)

		test.Error(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID: "cus_123", MeterName: "api_requests", Quantity: 1, IdempotencyKey: "mtr_abc",
		}))
	})
}

package noop

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v13/capitalism"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestPaymentManager_HandleEventWebhook(T *testing.T) {
	T.Parallel()

	T.Run("returns nil", func(t *testing.T) {
		t.Parallel()
		mgr := NewPaymentManager()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com/webhook", http.NoBody)
		must.NoError(t, err)

		test.NoError(t, mgr.HandleEventWebhook(req))
	})
}

func TestPaymentManager_ImplementsInterface(T *testing.T) {
	T.Parallel()

	T.Run("satisfies PaymentManager", func(t *testing.T) {
		t.Parallel()
		_ = NewPaymentManager()
	})
}

func TestUsageReporter_ReportUsage(T *testing.T) {
	T.Parallel()

	T.Run("returns nil", func(t *testing.T) {
		t.Parallel()

		// What a deployment that meters but does not bill runs: usage still
		// accumulates durably and quotas are still enforced, and nothing reaches a
		// provider. A normal configuration, not a degraded one.
		reporter := NewUsageReporter()

		test.NoError(t, reporter.ReportUsage(t.Context(), &capitalism.UsageReportInput{
			CustomerID: "cus_123", MeterName: "api_requests", Quantity: 1, IdempotencyKey: "mtr_abc",
		}))
		test.NoError(t, reporter.ReportUsage(t.Context(), nil))
	})
}

// Package noop holds the capitalism implementations a deployment that does not
// bill runs, and the two are deliberately unalike.
//
// NewPaymentManager is loud. CreateCustomer, CreatePaymentIntent, and
// CreateSubscription all report capitalism.ErrPaymentsDisabled rather than the
// empty IDs and nil errors they once returned, because an empty customer ID
// stored as though it were real is a bug that surfaces months later against a
// provider that has never heard of the account. Its webhook handler is the
// exception and accepts what it is given: no provider exists to have sent the
// event, so ignoring it is the honest answer.
//
// NewUsageReporter is quiet. ReportUsage succeeds and posts nowhere, which is
// what a deployment that meters internally without billing wants — usage still
// accumulates durably and quotas are still enforced. That is a normal
// configuration rather than a degraded one, which is why it does not error.
//
// capitalism/config builds either for the "noop" provider name.
package noop

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v13/capitalism"
)

var _ capitalism.PaymentManager = (*PaymentManager)(nil)

// PaymentManager is a no-op payment manager.
type PaymentManager struct{}

// HandleEventWebhook satisfies our interface. There is no provider to have
// sent the event, so accepting and ignoring it is the honest answer.
func (n *PaymentManager) HandleEventWebhook(_ *http.Request) error {
	return nil
}

// CreateCustomer satisfies our interface, reporting ErrPaymentsDisabled rather
// than an empty customer ID the caller would store as if it were real.
func (n *PaymentManager) CreateCustomer(_ context.Context, _ *capitalism.CustomerCreationInput) (string, error) {
	return "", capitalism.ErrPaymentsDisabled
}

// CreatePaymentIntent satisfies our interface, reporting ErrPaymentsDisabled
// rather than a zero intent that never charged anyone.
func (n *PaymentManager) CreatePaymentIntent(_ context.Context, _ *capitalism.PaymentIntentCreationInput) (*capitalism.PaymentIntent, error) {
	return nil, capitalism.ErrPaymentsDisabled
}

// CreateSubscription satisfies our interface, reporting ErrPaymentsDisabled
// rather than an empty subscription ID.
func (n *PaymentManager) CreateSubscription(_ context.Context, _ *capitalism.SubscriptionCreationInput) (string, error) {
	return "", capitalism.ErrPaymentsDisabled
}

// NewPaymentManager returns a no-op PaymentManager.
//
// Its webhook handler accepts and ignores; every operation that would create
// provider-side state returns capitalism.ErrPaymentsDisabled. It is what a
// disabled capitalism config yields, and it is deliberately loud: the previous
// behavior — empty IDs and nil errors — reported success for payments that
// never happened.
func NewPaymentManager() *PaymentManager {
	return &PaymentManager{}
}

var _ capitalism.UsageReporter = (*UsageReporter)(nil)

// UsageReporter is a no-op usage reporter.
type UsageReporter struct{}

// ReportUsage satisfies our interface.
func (n *UsageReporter) ReportUsage(_ context.Context, _ *capitalism.UsageReportInput) error {
	return nil
}

// NewUsageReporter returns a no-op UsageReporter.
//
// It is what a deployment that meters but does not bill runs: usage still
// accumulates durably and quotas are still enforced, and nothing is posted to a
// provider. That is a normal configuration — an internal quota system — rather
// than a degraded one.
func NewUsageReporter() *UsageReporter {
	return &UsageReporter{}
}

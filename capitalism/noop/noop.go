package noop

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v3/capitalism"
)

var _ capitalism.PaymentManager = (*paymentManager)(nil)

// paymentManager is a no-op payment manager.
type paymentManager struct{}

// HandleEventWebhook satisfies our interface.
func (n *paymentManager) HandleEventWebhook(_ *http.Request) error {
	return nil
}

// CreateCustomer satisfies our interface.
func (n *paymentManager) CreateCustomer(_ context.Context, _ *capitalism.CustomerCreationInput) (string, error) {
	return "", nil
}

// CreatePaymentIntent satisfies our interface.
func (n *paymentManager) CreatePaymentIntent(_ context.Context, _ *capitalism.PaymentIntentCreationInput) (*capitalism.PaymentIntent, error) {
	return &capitalism.PaymentIntent{}, nil
}

// CreateSubscription satisfies our interface.
func (n *paymentManager) CreateSubscription(_ context.Context, _ *capitalism.SubscriptionCreationInput) (string, error) {
	return "", nil
}

// NewPaymentManager returns a no-op PaymentManager.
func NewPaymentManager() capitalism.PaymentManager {
	return &paymentManager{}
}

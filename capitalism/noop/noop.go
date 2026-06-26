package noop

import (
	"net/http"

	"github.com/primandproper/platform-go/capitalism"
)

var _ capitalism.PaymentManager = (*paymentManager)(nil)

// paymentManager is a no-op payment manager.
type paymentManager struct{}

// HandleEventWebhook satisfies our interface.
func (n *paymentManager) HandleEventWebhook(_ *http.Request) error {
	return nil
}

// NewPaymentManager returns a no-op PaymentManager.
func NewPaymentManager() capitalism.PaymentManager {
	return &paymentManager{}
}

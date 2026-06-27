package stripe

import (
	"io"
	"net/http"

	"github.com/primandproper/platform-go/capitalism"
	"github.com/primandproper/platform-go/capitalism/noop"
	"github.com/primandproper/platform-go/encoding"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/webhook"
)

const (
	stripeSignatureHeaderKey = "Stripe-Signature"
	implementationName       = "stripe_payment_manager"
)

var _ capitalism.PaymentManager = (*stripePaymentManager)(nil)

type (
	// WebhookSecret is a string alias for dependency injection.
	WebhookSecret string
	// APIKey is a string alias for dependency injection.
	APIKey string

	stripePaymentManager struct {
		o11y           observability.Observer
		encoderDecoder encoding.ServerEncoderDecoder
		webhookSecret  string
	}
)

// ProvideStripePaymentManager builds a Stripe-backed PaymentManager.
func ProvideStripePaymentManager(logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config) capitalism.PaymentManager {
	if cfg == nil {
		return noop.NewPaymentManager()
	}

	return &stripePaymentManager{
		webhookSecret:  cfg.WebhookSecret,
		encoderDecoder: encoding.ProvideServerEncoderDecoder(logger, tracerProvider, encoding.ContentTypeJSON),
		o11y:           observability.NewObserver(implementationName, logger, tracerProvider),
	}
}

func (s *stripePaymentManager) HandleEventWebhook(req *http.Request) error {
	ctx, op := s.o11y.Begin(req.Context())
	defer op.End()

	payload, err := io.ReadAll(req.Body)
	if err != nil {
		return err
	}

	signatureHeader := req.Header.Get(stripeSignatureHeaderKey)
	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		return err
	}

	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		var paymentIntent stripe.PaymentIntent
		if decodeErr := s.encoderDecoder.DecodeBytes(ctx, event.Data.Raw, &paymentIntent); decodeErr != nil {
			return decodeErr
		}
	default:
		op.Set("event_type", event.Type)
		op.Logger().WithRequest(req).Info("Unhandled event type")
	}

	return nil
}

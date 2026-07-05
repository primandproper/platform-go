package stripe

import (
	"context"
	"io"
	"net/http"

	"github.com/primandproper/platform-go/v4/capitalism"
	"github.com/primandproper/platform-go/v4/encoding"
	platformerrors "github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/client"
	"github.com/stripe/stripe-go/v75/webhook"
)

const (
	stripeSignatureHeaderKey = "Stripe-Signature"
	implementationName       = "stripe_payment_manager"
	// maxWebhookBodyBytes bounds how much of a webhook request body we read; Stripe
	// event payloads are well under this, and it stops a hostile client from forcing
	// an unbounded allocation on this public endpoint.
	maxWebhookBodyBytes = 64 << 10 // 64 KiB
)

var (
	_ capitalism.PaymentManager = (*stripePaymentManager)(nil)

	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("stripe config is nil")
	// ErrAPIKeyNotConfigured indicates an outbound operation was attempted without a Stripe API
	// key. The webhook path needs only WebhookSecret, so the key is optional at construction;
	// outbound operations require it.
	ErrAPIKeyNotConfigured = platformerrors.New("stripe API key not configured; set the API key to use outbound operations")
)

type (
	// EventHandler is an optional callback invoked with each verified Stripe event, letting a
	// consumer act on a webhook (e.g. fulfill an order) after signature verification succeeds.
	// A nil handler leaves the default behavior (decode known events + log) in place.
	EventHandler func(ctx context.Context, event *stripe.Event) error

	stripePaymentManager struct {
		o11y           observability.Observer
		encoderDecoder encoding.ServerEncoderDecoder
		client         *client.API
		handler        EventHandler
		webhookSecret  string
	}
)

// ProvideStripePaymentManager builds a Stripe-backed PaymentManager. When cfg.APIKey is set, an API
// client is initialized for outbound operations; otherwise only the inbound webhook path works.
// handler is optional and invoked for every verified event.
func ProvideStripePaymentManager(logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config, handler EventHandler) (capitalism.PaymentManager, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	m := &stripePaymentManager{
		webhookSecret:  cfg.WebhookSecret,
		encoderDecoder: encoding.ProvideServerEncoderDecoder(logger, tracerProvider, encoding.ContentTypeJSON),
		o11y:           observability.NewObserver(implementationName, logger, tracerProvider),
		handler:        handler,
	}

	if cfg.APIKey != "" {
		sc := &client.API{}
		sc.Init(cfg.APIKey, nil)
		m.client = sc
	}

	return m, nil
}

func (s *stripePaymentManager) HandleEventWebhook(req *http.Request) error {
	ctx, op := s.o11y.Begin(req.Context())
	defer op.End()

	// Cap the body of this public, unauthenticated endpoint so a hostile client
	// can't exhaust memory with an arbitrarily large payload.
	payload, err := io.ReadAll(http.MaxBytesReader(nil, req.Body, maxWebhookBodyBytes))
	if err != nil {
		return op.Error(err, "reading webhook body")
	}

	signatureHeader := req.Header.Get(stripeSignatureHeaderKey)
	event, err := webhook.ConstructEvent(payload, signatureHeader, s.webhookSecret)
	if err != nil {
		return op.Error(err, "verifying webhook signature")
	}

	op.Set("stripe.event_id", event.ID).Set("stripe.event_type", event.Type)

	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		var paymentIntent stripe.PaymentIntent
		if decodeErr := s.encoderDecoder.DecodeBytes(ctx, event.Data.Raw, &paymentIntent); decodeErr != nil {
			return op.Error(decodeErr, "decoding payment intent")
		}

		op.Set("stripe.payment_intent_id", paymentIntent.ID).
			Set("stripe.amount", paymentIntent.Amount).
			Set("stripe.currency", paymentIntent.Currency)
	default:
		op.Set("event_type", event.Type)
		op.Logger().WithRequest(req).Info("Unhandled event type")
	}

	// Hand the verified event to the consumer callback (if any) so it can act on it, rather than
	// decoding it here and dropping it on the floor.
	if s.handler != nil {
		if err = s.handler(ctx, &event); err != nil {
			return op.Error(err, "handling stripe event")
		}
	}

	return nil
}

// CreateCustomer creates a Stripe customer.
func (s *stripePaymentManager) CreateCustomer(ctx context.Context, input *capitalism.CustomerCreationInput) (string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if input == nil {
		return "", op.Error(platformerrors.ErrNilInputParameter, "creating customer")
	}
	if s.client == nil {
		return "", op.Error(ErrAPIKeyNotConfigured, "creating customer")
	}

	params := &stripe.CustomerParams{Metadata: input.Metadata}
	if input.Email != "" {
		params.Email = new(input.Email)
	}
	if input.Name != "" {
		params.Name = new(input.Name)
	}
	applyRequestParams(&params.Params, ctx, input.IdempotencyKey)

	customer, err := s.client.Customers.New(params)
	if err != nil {
		return "", op.Error(err, "creating customer")
	}

	op.Set("stripe.customer_id", customer.ID)

	return customer.ID, nil
}

// CreatePaymentIntent creates a Stripe payment intent.
func (s *stripePaymentManager) CreatePaymentIntent(ctx context.Context, input *capitalism.PaymentIntentCreationInput) (*capitalism.PaymentIntent, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if input == nil {
		return nil, op.Error(platformerrors.ErrNilInputParameter, "creating payment intent")
	}
	if s.client == nil {
		return nil, op.Error(ErrAPIKeyNotConfigured, "creating payment intent")
	}

	params := &stripe.PaymentIntentParams{
		Amount:   new(input.Amount),
		Currency: new(input.Currency),
		Metadata: input.Metadata,
	}
	if input.CustomerID != "" {
		params.Customer = new(input.CustomerID)
	}
	if input.Description != "" {
		params.Description = new(input.Description)
	}
	applyRequestParams(&params.Params, ctx, input.IdempotencyKey)

	intent, err := s.client.PaymentIntents.New(params)
	if err != nil {
		return nil, op.Error(err, "creating payment intent")
	}

	op.Set("stripe.payment_intent_id", intent.ID)

	return &capitalism.PaymentIntent{ID: intent.ID, ClientSecret: intent.ClientSecret}, nil
}

// CreateSubscription creates a Stripe subscription for a customer on a single price.
func (s *stripePaymentManager) CreateSubscription(ctx context.Context, input *capitalism.SubscriptionCreationInput) (string, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if input == nil {
		return "", op.Error(platformerrors.ErrNilInputParameter, "creating subscription")
	}
	if s.client == nil {
		return "", op.Error(ErrAPIKeyNotConfigured, "creating subscription")
	}

	params := &stripe.SubscriptionParams{
		Customer: new(input.CustomerID),
		Items: []*stripe.SubscriptionItemsParams{
			{Price: new(input.PriceID)},
		},
		Metadata: input.Metadata,
	}
	applyRequestParams(&params.Params, ctx, input.IdempotencyKey)

	subscription, err := s.client.Subscriptions.New(params)
	if err != nil {
		return "", op.Error(err, "creating subscription")
	}

	op.Set("stripe.subscription_id", subscription.ID)

	return subscription.ID, nil
}

// applyRequestParams attaches the context and, when provided, an idempotency key to a Stripe
// request so a create is safely retryable.
func applyRequestParams(p *stripe.Params, ctx context.Context, idempotencyKey string) {
	p.Context = ctx
	if idempotencyKey != "" {
		p.SetIdempotencyKey(idempotencyKey)
	}
}

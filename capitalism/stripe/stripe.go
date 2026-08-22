package stripe

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
)

const (
	implementationName = "stripe_payment_manager"
	// maxWebhookBodyBytes bounds how much of a webhook request body we read; Stripe
	// event payloads are well under this, and it stops a hostile client from forcing
	// an unbounded allocation on this public endpoint.
	maxWebhookBodyBytes = 64 << 10 // 64 KiB
)

var (
	_ capitalism.PaymentManager = (*PaymentManager)(nil)

	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("stripe config is nil")
	// ErrAPIKeyNotConfigured indicates an outbound operation was attempted without a Stripe API
	// key. The webhook path needs only WebhookSecret, so the key is optional at construction;
	// outbound operations require it.
	ErrAPIKeyNotConfigured = platformerrors.New("stripe API key not configured; set the API key to use outbound operations")

	// ErrWebhookSecretNotConfigured indicates an inbound webhook arrived at a manager built
	// without one. The outbound operations need only APIKey, so the secret is optional at
	// construction; the webhook path requires it.
	//
	// It is its own error because the alternative — verifying under an empty secret — rejects
	// every delivery with a signature error, which reads as Stripe's fault rather than as a
	// missing environment variable.
	ErrWebhookSecretNotConfigured = platformerrors.New("stripe webhook secret not configured; set the webhook secret to receive events")
)

var _ capitalism.PaymentManager = (*PaymentManager)(nil)

type (
	// Event is a verified webhook event, in this module's own vocabulary.
	//
	// It exists so that stripe-go's types stay out of the exported API. A handler
	// typed on *stripe.Event pins every consumer of this module to the exact
	// stripe-go major it was built against, and turns each of that SDK's major
	// bumps into a breaking change for platform-go — for callers who never
	// mention Stripe.
	//
	// Payload is the raw JSON of the event's data object, exactly as it arrived.
	// A consumer that needs the typed struct decodes it with whatever stripe-go
	// version it chooses, on its own schedule.
	Event struct {
		// ID is the provider's event identifier, stable for deduplication.
		ID string
		// Type is the provider's event type, e.g. "payment_intent.succeeded".
		Type string
		// Payload is the raw JSON of the event's data object.
		Payload []byte
	}

	// EventHandler is an optional callback invoked with each verified Stripe event, letting a
	// consumer act on a webhook (e.g. fulfill an order) after signature verification succeeds.
	// A nil handler leaves the default behavior (decode known events + log) in place.
	EventHandler func(ctx context.Context, event *Event) error

	// PaymentManager is the Stripe capitalism.PaymentManager implementation. It is
	// exported, and returned by NewPaymentManager, so a caller who has chosen
	// Stripe can depend on that choice rather than on the interface every payment
	// processor shares.
	PaymentManager struct {
		o11y           observability.Observer
		encoderDecoder encoding.ServerEncoderDecoder
		client         *client.API
		handler        EventHandler
		instruments    *instruments
		verifier       inbound.Verifier
	}
)

// NewPaymentManager builds a Stripe-backed PaymentManager. When cfg.APIKey is set, an API
// client is initialized for outbound operations; when cfg.WebhookSecret is set, a verifier is
// built for the inbound webhook path. Either half works without the other.
// handler is optional and invoked for every verified event.
func NewPaymentManager(cfg *Config, handler EventHandler, opts ...Option) (*PaymentManager, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	o := newOptions(opts)

	instruments, err := newInstruments(o.metricsProvider)
	if err != nil {
		return nil, err
	}

	m := &PaymentManager{
		encoderDecoder: encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON, encoding.WithLogger(o.logger), encoding.WithTracerProvider(o.tracerProvider)),
		o11y:           observability.NewObserver(implementationName, o.logger, o.tracerProvider),
		handler:        handler,
		instruments:    instruments,
	}

	if cfg.APIKey != "" {
		sc := &client.API{}
		sc.Init(cfg.APIKey, nil)
		m.client = sc
	}

	if cfg.WebhookSecret != "" {
		verifier, verifierErr := inbound.NewStripeVerifier(cfg.WebhookSecret)
		if verifierErr != nil {
			return nil, platformerrors.Wrap(verifierErr, "building the stripe webhook verifier")
		}

		m.verifier = verifier
	}

	return m, nil
}

// HandleEventWebhook verifies an inbound Stripe delivery and hands it to the configured
// handler.
//
// Verification runs through webhooks/inbound's Stripe scheme rather than through stripe-go's,
// so this module has one implementation of the t=…,v1=… format — the same one a service that
// receives Stripe webhooks without a PaymentManager gets from inbound.NewStripeVerifier.
//
// This does the work inline, on the request's goroutine, and so couples Stripe's ack deadline
// to how long the handler takes. A service whose handler does anything slow should mount an
// inbound.Receiver instead, which publishes the delivery and acks; this path exists for
// handlers that are genuinely fast.
func (s *PaymentManager) HandleEventWebhook(req *http.Request) (err error) {
	ctx, op := s.o11y.Begin(req.Context())
	defer op.End()

	startedAt := time.Now()
	defer func() { s.instruments.record(ctx, opHandleWebhook, startedAt, err) }()

	if s.verifier == nil {
		return op.Error(ErrWebhookSecretNotConfigured, "verifying webhook signature")
	}

	// Cap the body of this public, unauthenticated endpoint so a hostile client
	// can't exhaust memory with an arbitrarily large payload.
	payload, err := io.ReadAll(http.MaxBytesReader(nil, req.Body, maxWebhookBodyBytes))
	if err != nil {
		return op.Error(err, "reading webhook body")
	}

	if err = s.verifier.Verify(ctx, req.Header, payload); err != nil {
		return op.Error(err, "verifying webhook signature")
	}

	// Decoded only after verification, so nothing here ever parses bytes Stripe did not send.
	var event stripe.Event
	if err = s.encoderDecoder.DecodeBytes(ctx, payload, &event); err != nil {
		return op.Error(err, "decoding webhook event")
	}

	op.Set("stripe.event_id", event.ID).Set("stripe.event_type", event.Type)

	// Every Stripe event carries a data object, but the field is a pointer and this is a
	// public endpoint: a delivery signed under the right secret can still be shaped however
	// its sender liked, and dereferencing on trust would turn that into a panic.
	var raw []byte
	if event.Data != nil {
		raw = event.Data.Raw
	}

	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		var paymentIntent stripe.PaymentIntent
		if decodeErr := s.encoderDecoder.DecodeBytes(ctx, raw, &paymentIntent); decodeErr != nil {
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
		if err = s.handler(ctx, &Event{
			ID:      event.ID,
			Type:    string(event.Type),
			Payload: raw,
		}); err != nil {
			return op.Error(err, "handling stripe event")
		}
	}

	return nil
}

// CreateCustomer creates a Stripe customer.
func (s *PaymentManager) CreateCustomer(ctx context.Context, input *capitalism.CustomerCreationInput) (_ string, err error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	startedAt := time.Now()
	defer func() { s.instruments.record(ctx, opCreateCustomer, startedAt, err) }()

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
func (s *PaymentManager) CreatePaymentIntent(ctx context.Context, input *capitalism.PaymentIntentCreationInput) (_ *capitalism.PaymentIntent, err error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	startedAt := time.Now()
	defer func() { s.instruments.record(ctx, opCreatePaymentIntent, startedAt, err) }()

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
func (s *PaymentManager) CreateSubscription(ctx context.Context, input *capitalism.SubscriptionCreationInput) (_ string, err error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	startedAt := time.Now()
	defer func() { s.instruments.record(ctx, opCreateSubscription, startedAt, err) }()

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

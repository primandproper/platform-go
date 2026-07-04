package capitalism

import (
	"context"
	"net/http"
)

type (
	// PaymentManager handles payments via 3rd-party providers.
	PaymentManager interface {
		// HandleEventWebhook verifies and processes an inbound provider webhook (e.g. Stripe events).
		HandleEventWebhook(req *http.Request) error
		// CreateCustomer creates a customer with the provider and returns its provider-assigned ID.
		CreateCustomer(ctx context.Context, input *CustomerCreationInput) (string, error)
		// CreatePaymentIntent creates a payment intent (a single charge in progress) and returns it.
		CreatePaymentIntent(ctx context.Context, input *PaymentIntentCreationInput) (*PaymentIntent, error)
		// CreateSubscription subscribes a customer to a price/plan and returns the subscription ID.
		CreateSubscription(ctx context.Context, input *SubscriptionCreationInput) (string, error)
	}

	// CustomerCreationInput describes a customer to create. All fields are optional except where a
	// provider requires them; IdempotencyKey, when set, makes the create safely retryable.
	CustomerCreationInput struct {
		Metadata       map[string]string
		Email          string
		Name           string
		IdempotencyKey string
	}

	// PaymentIntentCreationInput describes a payment to initiate. Amount is in the smallest unit of
	// Currency (e.g. cents for USD). IdempotencyKey, when set, makes the create safely retryable.
	PaymentIntentCreationInput struct {
		Metadata       map[string]string
		Currency       string
		CustomerID     string
		Description    string
		IdempotencyKey string
		Amount         int64
	}

	// PaymentIntent is the result of creating a payment intent. ClientSecret is handed to a client
	// SDK to complete the payment.
	PaymentIntent struct {
		ID           string
		ClientSecret string
	}

	// SubscriptionCreationInput describes a subscription to create: a customer subscribed to a
	// single price. IdempotencyKey, when set, makes the create safely retryable.
	SubscriptionCreationInput struct {
		Metadata       map[string]string
		CustomerID     string
		PriceID        string
		IdempotencyKey string
	}
)

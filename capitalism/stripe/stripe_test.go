package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	encodingmock "github.com/primandproper/platform-go/v13/encoding/mock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/random"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/webhook"
)

// stripeAPIVersion is the API version the vendored stripe-go expects on a
// webhook event.
//
// It has to be spelled out here because the SDK keeps its own copy unexported and
// rejects an event stamped with any other version. A stripe-go bump that moves it
// fails these tests, which is the point: the same mismatch would reject every real
// webhook until the endpoint's version was moved too.
const stripeAPIVersion = "2025-02-24.acacia"

type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("read error") }
func (*errReader) Close() error             { return nil }

func TestNewPaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		pm, err := NewPaymentManager(&Config{}, nil)

		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		pm, err := NewPaymentManager(nil, nil)

		test.Error(t, err)
		test.Nil(t, pm)
	})
}

// withWebhookSecret points pm's verifier at secret, standing in for the construction the
// config path does.
func withWebhookSecret(t *testing.T, pm *PaymentManager, secret string) {
	t.Helper()

	verifier, err := inbound.NewStripeVerifier(secret)
	must.NoError(t, err)

	pm.verifier = verifier
}

// signedWebhookRequest builds a request carrying event, signed under secret.
//
// The header comes from stripe-go's own test helper rather than from this module, which is
// what makes these tests a cross-check: the SDK signs, webhooks/inbound verifies, and the two
// agreeing is the evidence that the scheme extracted onto the seam is the same scheme.
//
// It must be called before any test swaps pm's encoder for a mock — the bytes that get signed
// are the ones the real encoder produces.
func signedWebhookRequest(t *testing.T, pm *PaymentManager, secret string, event *stripe.Event) *http.Request {
	t.Helper()

	ctx := t.Context()

	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   pm.encoderDecoder.MustEncode(ctx, event),
		Secret:    secret,
		Timestamp: time.Now(),
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(signed.Payload))
	must.NoError(t, err)
	req.Header.Set(inbound.StripeSignatureHeader, signed.Header)

	return req
}

// newWebhookManager builds a manager whose verifier trusts a freshly generated secret, and
// returns both.
func newWebhookManager(t *testing.T) (pm *PaymentManager, secret string) {
	t.Helper()

	secret, err := random.GenerateHexEncodedString(t.Context(), 32)
	must.NoError(t, err)
	must.NotEq(t, "", secret)

	manager, err := NewPaymentManager(&Config{}, nil)
	must.NoError(t, err)

	withWebhookSecret(t, manager, secret)

	return manager, secret
}

// paymentIntentEvent is a payment_intent.succeeded event carrying intent.
func paymentIntentEvent(t *testing.T, id string, intent *stripe.PaymentIntent) *stripe.Event {
	t.Helper()

	raw, err := json.Marshal(intent)
	must.NoError(t, err)

	return &stripe.Event{
		APIVersion: stripeAPIVersion,
		ID:         id,
		Data:       &stripe.EventData{Raw: json.RawMessage(raw)},
		Type:       stripe.EventTypePaymentIntentSucceeded,
	}
}

func Test_stripePaymentManager_HandleEventWebhook(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		pm, secret := newWebhookManager(t)

		req := signedWebhookRequest(t, pm, secret, paymentIntentEvent(t, "evt_123", &stripe.PaymentIntent{
			ID:       "pi_123",
			Amount:   4200,
			Currency: "usd",
		}))

		obs := observability.NewRecordingObserver()
		pm.o11y = obs

		test.NoError(t, pm.HandleEventWebhook(req))

		obs.ObservedOperationWithData(t, map[string]any{
			"stripe.event_id":          "evt_123",
			"stripe.payment_intent_id": "pi_123",
			"stripe.amount":            int64(4200),
			"stripe.currency":          stripe.Currency("usd"),
		})
	})

	T.Run("with no webhook secret configured", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pm, err := NewPaymentManager(&Config{}, nil)
		must.NoError(t, err)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader([]byte(`{}`)))
		must.NoError(t, err)

		// Not a signature error: the endpoint is misconfigured, and reporting it as a bad
		// signature would blame Stripe for a missing environment variable.
		test.ErrorIs(t, pm.HandleEventWebhook(req), ErrWebhookSecretNotConfigured)
	})

	T.Run("with error reading body", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pm, _ := newWebhookManager(t)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", http.NoBody)
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Body = &errReader{}

		test.Error(t, pm.HandleEventWebhook(req))
	})

	T.Run("with oversized body", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pm, _ := newWebhookManager(t)

		// A body larger than the cap must be rejected rather than read into memory.
		oversized := bytes.Repeat([]byte("a"), maxWebhookBodyBytes+1)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(oversized))
		must.NoError(t, err)
		req.Header.Set(inbound.StripeSignatureHeader, "sig")

		test.Error(t, pm.HandleEventWebhook(req))
	})

	T.Run("with invalid signature", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pm, _ := newWebhookManager(t)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader([]byte(`{}`)))
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Header.Set(inbound.StripeSignatureHeader, "invalid_signature")

		test.ErrorIs(t, pm.HandleEventWebhook(req), inbound.ErrInvalidSignature)
	})

	T.Run("with a signature minted under a different secret", func(t *testing.T) {
		t.Parallel()

		pm, _ := newWebhookManager(t)

		other, err := random.GenerateHexEncodedString(t.Context(), 32)
		must.NoError(t, err)

		req := signedWebhookRequest(t, pm, other, paymentIntentEvent(t, "evt_123", &stripe.PaymentIntent{}))

		test.ErrorIs(t, pm.HandleEventWebhook(req), inbound.ErrInvalidSignature)
	})

	T.Run("with decode error for the event", func(t *testing.T) {
		t.Parallel()

		pm, secret := newWebhookManager(t)

		req := signedWebhookRequest(t, pm, secret, paymentIntentEvent(t, "evt_123", &stripe.PaymentIntent{}))

		encoderDecoder := &encodingmock.ServerEncoderDecoderMock{
			DecodeBytesFunc: func(context.Context, []byte, any) error {
				return fmt.Errorf("decode error")
			},
		}
		pm.encoderDecoder = encoderDecoder

		test.Error(t, pm.HandleEventWebhook(req))
		test.SliceLen(t, 1, encoderDecoder.DecodeBytesCalls())
	})

	T.Run("with decode error for payment intent", func(t *testing.T) {
		t.Parallel()

		pm, secret := newWebhookManager(t)

		req := signedWebhookRequest(t, pm, secret, paymentIntentEvent(t, "evt_123", &stripe.PaymentIntent{}))

		// The envelope decodes for real and the nested object fails, which is the only way to
		// reach the second decode at all: an envelope that decodes to nothing carries no event
		// type, and the switch falls through to the unhandled branch.
		passthrough := pm.encoderDecoder
		encoderDecoder := &encodingmock.ServerEncoderDecoderMock{}
		encoderDecoder.DecodeBytesFunc = func(ctx context.Context, content []byte, v any) error {
			if len(encoderDecoder.DecodeBytesCalls()) == 1 {
				return passthrough.DecodeBytes(ctx, content, v)
			}

			return fmt.Errorf("decode error")
		}
		pm.encoderDecoder = encoderDecoder

		test.Error(t, pm.HandleEventWebhook(req))
		test.SliceLen(t, 2, encoderDecoder.DecodeBytesCalls())
	})

	T.Run("with unhandled event type", func(t *testing.T) {
		t.Parallel()

		pm, secret := newWebhookManager(t)

		req := signedWebhookRequest(t, pm, secret, &stripe.Event{
			APIVersion: stripeAPIVersion,
			Data:       &stripe.EventData{Raw: json.RawMessage(`{}`)},
			Type:       stripe.EventTypeAccountUpdated,
		})

		obs := observability.NewRecordingObserver()
		pm.o11y = obs

		test.NoError(t, pm.HandleEventWebhook(req))

		obs.ObservedOperationWithData(t, map[string]any{
			"event_type": stripe.EventTypeAccountUpdated,
		})
	})

	T.Run("with an event carrying no data object", func(t *testing.T) {
		t.Parallel()

		pm, secret := newWebhookManager(t)

		// A verified delivery is still whatever its sender chose to send. Nothing here may
		// dereference the data pointer on trust.
		req := signedWebhookRequest(t, pm, secret, &stripe.Event{
			APIVersion: stripeAPIVersion,
			ID:         "evt_123",
			Type:       stripe.EventTypeAccountUpdated,
		})

		test.NoError(t, pm.HandleEventWebhook(req))
	})
}

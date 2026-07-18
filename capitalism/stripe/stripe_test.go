package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	mockencoding "github.com/primandproper/platform-go/v5/encoding/mock"
	"github.com/primandproper/platform-go/v5/observability"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/random"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/stripe/stripe-go/v75"
	"github.com/stripe/stripe-go/v75/webhook"
)

type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("read error") }
func (*errReader) Close() error             { return nil }

func TestNewStripePaymentManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		pm, err := NewStripePaymentManager(logger, tracingnoop.NewTracerProvider(), &Config{}, nil)

		must.NoError(t, err)
		test.NotNil(t, pm)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		pm, err := NewStripePaymentManager(logger, tracingnoop.NewTracerProvider(), nil, nil)

		test.Error(t, err)
		test.Nil(t, pm)
	})
}

func Test_stripePaymentManager_HandleSubscriptionEventWebhook(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)

		paymentIntent := &stripe.PaymentIntent{
			APIResource:      stripe.APIResource{},
			Amount:           0,
			AmountCapturable: 0,
			AmountDetails:    nil,
			AmountReceived:   0,
			Customer:         nil,
			ID:               "",
			Invoice:          nil,
			Metadata:         nil,
			PaymentMethod:    nil,
			ReceiptEmail:     "",
			Status:           "",
		}

		rawMessage, err := json.Marshal(paymentIntent)
		must.NoError(t, err)
		must.NotNil(t, rawMessage)

		exampleInput := &stripe.Event{
			APIResource: stripe.APIResource{},
			Account:     "",
			APIVersion:  "2023-08-16",
			Created:     0,
			Data: &stripe.EventData{
				Object:             nil,
				PreviousAttributes: nil,
				Raw:                json.RawMessage(rawMessage),
			},
			ID:              "",
			Livemode:        false,
			Object:          "",
			PendingWebhooks: 0,
			Request:         nil,
			Type:            stripe.EventTypePaymentIntentSucceeded,
		}
		jsonBytes := pm.encoderDecoder.MustEncode(ctx, exampleInput)

		secret, err := random.GenerateHexEncodedString(ctx, 32)
		must.NoError(t, err)
		must.NotEq(t, "", secret)
		pm.webhookSecret = secret

		now := time.Now()
		signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
			Payload:   jsonBytes,
			Secret:    secret,
			Timestamp: now,
		})

		event, err := webhook.ConstructEvent(signedPayload.Payload, signedPayload.Header, signedPayload.Secret)
		must.NoError(t, err)
		eventPayload := pm.encoderDecoder.MustEncode(ctx, event)

		encoderDecoder := &mockencoding.ServerEncoderDecoderMock{
			DecodeBytesFunc: func(_ context.Context, _ []byte, _ any) error {
				return nil
			},
		}
		pm.encoderDecoder = encoderDecoder

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(eventPayload))
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Header.Set(stripeSignatureHeaderKey, signedPayload.Header)

		err = pm.HandleEventWebhook(req)
		test.NoError(t, err)

		test.SliceLen(t, 1, encoderDecoder.DecodeBytesCalls())
	})

	T.Run("with error reading body", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", http.NoBody)
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Body = &errReader{}

		err = pm.HandleEventWebhook(req)
		test.Error(t, err)
	})

	T.Run("with oversized body", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)
		pm.webhookSecret = "some_secret"

		// A body larger than the cap must be rejected rather than read into memory.
		oversized := bytes.Repeat([]byte("a"), (64<<10)+1)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(oversized))
		must.NoError(t, err)
		req.Header.Set(stripeSignatureHeaderKey, "sig")

		err = pm.HandleEventWebhook(req)
		test.Error(t, err)
	})

	T.Run("with invalid signature", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)
		pm.webhookSecret = "some_secret"

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader([]byte(`{}`)))
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Header.Set(stripeSignatureHeaderKey, "invalid_signature")

		err = pm.HandleEventWebhook(req)
		test.Error(t, err)
	})

	T.Run("with decode error for payment intent", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)

		paymentIntent := &stripe.PaymentIntent{}

		rawMessage, err := json.Marshal(paymentIntent)
		must.NoError(t, err)

		exampleInput := &stripe.Event{
			APIVersion: "2023-08-16",
			Data: &stripe.EventData{
				Raw: json.RawMessage(rawMessage),
			},
			Type: stripe.EventTypePaymentIntentSucceeded,
		}
		jsonBytes := pm.encoderDecoder.MustEncode(ctx, exampleInput)

		secret, err := random.GenerateHexEncodedString(ctx, 32)
		must.NoError(t, err)
		must.NotEq(t, "", secret)
		pm.webhookSecret = secret

		signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
			Payload:   jsonBytes,
			Secret:    secret,
			Timestamp: time.Now(),
		})

		event, err := webhook.ConstructEvent(signedPayload.Payload, signedPayload.Header, signedPayload.Secret)
		must.NoError(t, err)
		eventPayload := pm.encoderDecoder.MustEncode(ctx, event)

		encoderDecoder := &mockencoding.ServerEncoderDecoderMock{
			DecodeBytesFunc: func(_ context.Context, _ []byte, _ any) error {
				return fmt.Errorf("decode error")
			},
		}
		pm.encoderDecoder = encoderDecoder

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(eventPayload))
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Header.Set(stripeSignatureHeaderKey, signedPayload.Header)

		err = pm.HandleEventWebhook(req)
		test.Error(t, err)

		test.SliceLen(t, 1, encoderDecoder.DecodeBytesCalls())
	})

	T.Run("with unhandled event type", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		pmIface, err := NewStripePaymentManager(nil, nil, &Config{}, nil)
		must.NoError(t, err)
		pm := pmIface.(*stripePaymentManager)

		obs := observability.NewRecordingObserver()
		pm.o11y = obs

		exampleInput := &stripe.Event{
			APIVersion: "2023-08-16",
			Data: &stripe.EventData{
				Raw: json.RawMessage(`{}`),
			},
			Type: stripe.EventTypeAccountUpdated,
		}
		jsonBytes := pm.encoderDecoder.MustEncode(ctx, exampleInput)

		secret, err := random.GenerateHexEncodedString(ctx, 32)
		must.NoError(t, err)
		must.NotEq(t, "", secret)
		pm.webhookSecret = secret

		signedPayload := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
			Payload:   jsonBytes,
			Secret:    secret,
			Timestamp: time.Now(),
		})

		event, err := webhook.ConstructEvent(signedPayload.Payload, signedPayload.Header, signedPayload.Secret)
		must.NoError(t, err)
		eventPayload := pm.encoderDecoder.MustEncode(ctx, event)

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://whatever.whocares.gov", bytes.NewReader(eventPayload))
		must.NoError(t, err)
		must.NotNil(t, req)
		req.Header.Set(stripeSignatureHeaderKey, signedPayload.Header)

		err = pm.HandleEventWebhook(req)
		test.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			"event_type": stripe.EventTypeAccountUpdated,
		})
	})
}

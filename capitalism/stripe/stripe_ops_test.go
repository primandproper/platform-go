package stripe

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/random"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/stripe/stripe-go/v81"
	"github.com/stripe/stripe-go/v81/client"
	"github.com/stripe/stripe-go/v81/webhook"
)

var errArbitraryHandler = platformerrors.New("arbitrary handler error")

type capturedRequest struct {
	form           url.Values
	method         string
	path           string
	idempotencyKey string
}

// newTestManager builds a stripePaymentManager whose Stripe client talks to an httptest server, so
// a test can drive the outbound operations and inspect the request Stripe would have sent. respond
// returns the (status, JSON body) for a given request path.
func newTestManager(t *testing.T, respond func(path string) (int, string)) (*PaymentManager, *[]capturedRequest) {
	t.Helper()

	var (
		mu       sync.Mutex
		captured []capturedRequest
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		captured = append(captured, capturedRequest{
			method:         r.Method,
			path:           r.URL.Path,
			form:           r.Form,
			idempotencyKey: r.Header.Get("Idempotency-Key"),
		})
		mu.Unlock()

		status, body := respond(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)

	backend := stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{URL: new(ts.URL)})
	sc := &client.API{}
	sc.Init("sk_test_123", &stripe.Backends{API: backend, Connect: backend, Uploads: backend})

	instruments, err := newInstruments(metricsnoop.NewMetricsProvider())
	must.NoError(t, err)

	pm := &PaymentManager{
		client:         sc,
		encoderDecoder: encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
		o11y:           observability.NewObserver(implementationName, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider()),
		instruments:    instruments,
	}

	return pm, &captured
}

func TestStripePaymentManager_CreatePaymentIntent(T *testing.T) {
	T.Parallel()

	T.Run("sends the correct request", func(t *testing.T) {
		t.Parallel()

		pm, captured := newTestManager(t, func(string) (int, string) {
			return http.StatusOK, `{"id":"pi_test","client_secret":"cs_test","object":"payment_intent"}`
		})

		result, err := pm.CreatePaymentIntent(t.Context(), &capitalism.PaymentIntentCreationInput{
			Amount:         1000,
			Currency:       "usd",
			CustomerID:     "cus_abc",
			Description:    "a widget",
			Metadata:       map[string]string{"order_id": "o-42"},
			IdempotencyKey: "idem-pi-1",
		})
		must.NoError(t, err)

		test.EqOp(t, "pi_test", result.ID)
		test.EqOp(t, "cs_test", result.ClientSecret)

		reqs := *captured
		must.SliceLen(t, 1, reqs)
		got := reqs[0]
		test.EqOp(t, http.MethodPost, got.method)
		test.EqOp(t, "/v1/payment_intents", got.path)
		test.EqOp(t, "1000", got.form.Get("amount"))
		test.EqOp(t, "usd", got.form.Get("currency"))
		test.EqOp(t, "cus_abc", got.form.Get("customer"))
		test.EqOp(t, "a widget", got.form.Get("description"))
		test.EqOp(t, "o-42", got.form.Get("metadata[order_id]"))
		test.EqOp(t, "idem-pi-1", got.idempotencyKey)
	})

	T.Run("errors without an API key", func(t *testing.T) {
		t.Parallel()

		pm, err := NewPaymentManager(&Config{WebhookSecret: "whsec"}, nil)
		must.NoError(t, err)

		result, err := pm.CreatePaymentIntent(t.Context(), &capitalism.PaymentIntentCreationInput{Amount: 1, Currency: "usd"})
		test.Error(t, err)
		test.Nil(t, result)
	})

	T.Run("errors on nil input", func(t *testing.T) {
		t.Parallel()

		pm, _ := newTestManager(t, func(string) (int, string) { return http.StatusOK, `{}` })

		result, err := pm.CreatePaymentIntent(t.Context(), nil)
		test.Error(t, err)
		test.Nil(t, result)
	})
}

func TestStripePaymentManager_CreateCustomer(T *testing.T) {
	T.Parallel()

	T.Run("sends the correct request", func(t *testing.T) {
		t.Parallel()

		pm, captured := newTestManager(t, func(string) (int, string) {
			return http.StatusOK, `{"id":"cus_test","object":"customer"}`
		})

		id, err := pm.CreateCustomer(t.Context(), &capitalism.CustomerCreationInput{
			Email:          "buyer@example.com",
			Name:           "Buyer Person",
			Metadata:       map[string]string{"tier": "gold"},
			IdempotencyKey: "idem-cus-1",
		})
		must.NoError(t, err)
		test.EqOp(t, "cus_test", id)

		reqs := *captured
		must.SliceLen(t, 1, reqs)
		got := reqs[0]
		test.EqOp(t, http.MethodPost, got.method)
		test.EqOp(t, "/v1/customers", got.path)
		test.EqOp(t, "buyer@example.com", got.form.Get("email"))
		test.EqOp(t, "Buyer Person", got.form.Get("name"))
		test.EqOp(t, "gold", got.form.Get("metadata[tier]"))
		test.EqOp(t, "idem-cus-1", got.idempotencyKey)
	})

	T.Run("errors without an API key", func(t *testing.T) {
		t.Parallel()

		pm, err := NewPaymentManager(&Config{WebhookSecret: "whsec"}, nil)
		must.NoError(t, err)

		id, err := pm.CreateCustomer(t.Context(), &capitalism.CustomerCreationInput{Email: "x@y.z"})
		test.Error(t, err)
		test.EqOp(t, "", id)
	})

	T.Run("errors on nil input", func(t *testing.T) {
		t.Parallel()

		pm, _ := newTestManager(t, func(string) (int, string) { return http.StatusOK, `{}` })

		id, err := pm.CreateCustomer(t.Context(), nil)
		test.Error(t, err)
		test.EqOp(t, "", id)
	})

	T.Run("errors when the Stripe API rejects the request", func(t *testing.T) {
		t.Parallel()

		pm, _ := newTestManager(t, func(string) (int, string) {
			return http.StatusBadRequest, `{"error":{"message":"boom","type":"invalid_request_error"}}`
		})

		id, err := pm.CreateCustomer(t.Context(), &capitalism.CustomerCreationInput{Email: "buyer@example.com"})
		test.Error(t, err)
		test.EqOp(t, "", id)
	})
}

func TestStripePaymentManager_CreateSubscription(T *testing.T) {
	T.Parallel()

	T.Run("sends the correct request", func(t *testing.T) {
		t.Parallel()

		pm, captured := newTestManager(t, func(string) (int, string) {
			return http.StatusOK, `{"id":"sub_test","object":"subscription"}`
		})

		id, err := pm.CreateSubscription(t.Context(), &capitalism.SubscriptionCreationInput{
			CustomerID:     "cus_abc",
			PriceID:        "price_xyz",
			IdempotencyKey: "idem-sub-1",
		})
		must.NoError(t, err)
		test.EqOp(t, "sub_test", id)

		reqs := *captured
		must.SliceLen(t, 1, reqs)
		got := reqs[0]
		test.EqOp(t, http.MethodPost, got.method)
		test.EqOp(t, "/v1/subscriptions", got.path)
		test.EqOp(t, "cus_abc", got.form.Get("customer"))
		test.EqOp(t, "price_xyz", got.form.Get("items[0][price]"))
		test.EqOp(t, "idem-sub-1", got.idempotencyKey)
	})

	T.Run("errors without an API key", func(t *testing.T) {
		t.Parallel()

		pm, err := NewPaymentManager(&Config{WebhookSecret: "whsec"}, nil)
		must.NoError(t, err)

		id, err := pm.CreateSubscription(t.Context(), &capitalism.SubscriptionCreationInput{CustomerID: "cus_abc", PriceID: "price_xyz"})
		test.Error(t, err)
		test.EqOp(t, "", id)
	})
}

func TestStripePaymentManager_HandleEventWebhook_Callback(T *testing.T) {
	T.Parallel()

	signedRequest := func(t *testing.T, pm *PaymentManager, secret string) *http.Request {
		t.Helper()

		ctx := t.Context()
		event := &stripe.Event{
			APIVersion: stripeAPIVersion,
			ID:         "evt_test_123",
			Data:       &stripe.EventData{Raw: []byte(`{}`)},
			Type:       stripe.EventTypePaymentIntentSucceeded,
		}
		// Signed with stripe-go's own test helper, which is what makes this a cross-check:
		// the header comes from the SDK and the verification comes from webhooks/inbound, so
		// the two agreeing is evidence the extracted scheme is the same scheme.
		signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
			Payload:   pm.encoderDecoder.MustEncode(ctx, event),
			Secret:    secret,
			Timestamp: time.Now(),
		})

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/webhook", bytes.NewReader(signed.Payload))
		must.NoError(t, err)
		req.Header.Set(inbound.StripeSignatureHeader, signed.Header)

		return req
	}

	T.Run("invokes the handler with the verified event", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 32)
		must.NoError(t, err)

		var (
			called bool
			gotID  string
		)
		handler := func(_ context.Context, event *Event) error {
			called = true
			gotID = event.ID
			return nil
		}

		pm, err := NewPaymentManager(&Config{WebhookSecret: secret}, handler)
		must.NoError(t, err)

		must.NoError(t, pm.HandleEventWebhook(signedRequest(t, pm, secret)))

		test.True(t, called)
		test.EqOp(t, "evt_test_123", gotID)
	})

	T.Run("propagates a handler error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		secret, err := random.GenerateHexEncodedString(ctx, 32)
		must.NoError(t, err)

		handler := func(_ context.Context, _ *Event) error {
			return errArbitraryHandler
		}

		pm, err := NewPaymentManager(&Config{WebhookSecret: secret}, handler)
		must.NoError(t, err)

		test.Error(t, pm.HandleEventWebhook(signedRequest(t, pm, secret)))
	})
}

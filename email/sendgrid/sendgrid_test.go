package sendgrid

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/email"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"github.com/shoenig/test/must"
)

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed.
func newRecordingEmailer(t *testing.T, cfg *Config, client *http.Client) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewSendGridEmailer(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
	must.NotNil(t, c)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func testEmailMessage(t *testing.T) *email.OutboundEmailMessage {
	t.Helper()

	return &email.OutboundEmailMessage{
		ToAddress:   t.Name(),
		ToName:      t.Name(),
		FromAddress: t.Name(),
		FromName:    t.Name(),
		Subject:     t.Name(),
		HTMLContent: t.Name(),
	}
}

func TestNewSendGridEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		client, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, client)
		must.NoError(t, err)
	})
}

func TestSendGridEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))
		client := ts.Client()
		client.Timeout = time.Millisecond

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, client)
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		// Even though the send failed, the values must still have been observed.
		// This package records failures span-only via observability.PrepareError,
		// so op.Errors stays empty by design.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("with invalid response code", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})
}

func TestSendGridEmailer_sendDynamicTemplateEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		ctx := t.Context()
		to := mail.NewEmail("sender", "sender@fake.com")
		from := mail.NewEmail("sender", "sender@fake.com")

		request := sendgrid.GetRequest(c.config.APIToken, "/v3/mail/send", ts.URL)

		must.NoError(t, c.sendDynamicTemplateEmail(ctx, to, from, t.Name(), map[string]any{"things": "stuff"}, request))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: to.Address,
		})
	})
}
